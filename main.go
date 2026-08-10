// webhook-relay receives webhook POSTs, renders them through per-webhook
// text/template definitions, and commits the result to concrnt v2 timelines
// as messages signed with the relay account's master key.
package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"text/template"
	"time"

	concrnt "github.com/concrnt/concrnt"
	"github.com/concrnt/concrnt/cdid"
	"gopkg.in/yaml.v3"
)

const (
	schemaMarkdown = "https://schema.concrnt.world/m/markdown.json"
	maxBodyLength  = 4096
)

type Config struct {
	Listen  string `yaml:"listen"`
	Concrnt struct {
		Domain     string `yaml:"domain"`
		CCID       string `yaml:"ccid"`
		PrivateKey string `yaml:"privateKey"`
	} `yaml:"concrnt"`
	Webhooks []Webhook `yaml:"webhooks"`
}

type Webhook struct {
	Path            string           `yaml:"path"`
	Schema          string           `yaml:"schema"`
	Timelines       []string         `yaml:"timelines"`
	ProfileOverride *ProfileOverride `yaml:"profileOverride"`
	Template        string           `yaml:"template"`
}

// MessageValue mirrors the m/markdown.json message schema family; those set
// additionalProperties: false, so optional fields must be omitempty.
type MessageValue struct {
	Body            string           `json:"body"`
	ProfileOverride *ProfileOverride `json:"profileOverride,omitempty"`
}

type ProfileOverride struct {
	Username    string `yaml:"username" json:"username,omitempty"`
	Avatar      string `yaml:"avatar" json:"avatar,omitempty"`
	Description string `yaml:"description" json:"description,omitempty"`
	Link        string `yaml:"link" json:"link,omitempty"`
}

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	raw, err := os.ReadFile(*configPath)
	if err != nil {
		log.Fatalf("failed to read config: %v", err)
	}
	var config Config
	if err := yaml.Unmarshal(raw, &config); err != nil {
		log.Fatalf("failed to parse config: %v", err)
	}
	if pk := os.Getenv("PRIVATE_KEY"); pk != "" {
		config.Concrnt.PrivateKey = pk
	}
	if config.Listen == "" {
		config.Listen = ":8080"
	}
	if config.Concrnt.Domain == "" {
		log.Fatal("concrnt.domain is required")
	}
	if config.Concrnt.PrivateKey == "" {
		log.Fatal("concrnt.privateKey (or PRIVATE_KEY env) is required")
	}

	ccid, err := concrnt.PrivKeyToAddr(config.Concrnt.PrivateKey, "con")
	if err != nil {
		log.Fatalf("invalid private key: %v", err)
	}
	if config.Concrnt.CCID != "" && config.Concrnt.CCID != ccid {
		log.Fatalf("concrnt.ccid %s does not match the private key (expects %s)", config.Concrnt.CCID, ccid)
	}

	base := config.Concrnt.Domain
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	commitURL := strings.TrimRight(base, "/") + "/api/v2/commit"
	httpClient := &http.Client{Timeout: 20 * time.Second}

	mux := http.NewServeMux()
	for _, webhook := range config.Webhooks {
		if webhook.Path == "" || webhook.Template == "" {
			log.Fatalf("webhook entries require both path and template: %+v", webhook)
		}
		tmpl, err := template.New(webhook.Path).Parse(webhook.Template)
		if err != nil {
			log.Fatalf("failed to parse template for %s: %v", webhook.Path, err)
		}
		schema := webhook.Schema
		if schema == "" {
			schema = schemaMarkdown
		}
		webhook := webhook

		mux.HandleFunc("POST "+webhook.Path, func(w http.ResponseWriter, r *http.Request) {
			payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
			if err != nil {
				http.Error(w, "failed to read body", http.StatusBadRequest)
				return
			}
			var body any
			if err := json.Unmarshal(payload, &body); err != nil {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}
			// Express lowercases header names; keep templates portable from v1.
			headers := map[string]string{}
			for name, values := range r.Header {
				headers[strings.ToLower(name)] = values[0]
			}

			var rendered bytes.Buffer
			if err := tmpl.Execute(&rendered, map[string]any{"body": body, "headers": headers}); err != nil {
				log.Printf("%s: template error: %v", webhook.Path, err)
				http.Error(w, "template error", http.StatusInternalServerError)
				return
			}
			result := rendered.String()
			if len(result) == 0 || len(result) > maxBodyLength {
				// Deliberately 200: don't make the sender retry an unpostable payload.
				log.Printf("%s: invalid length %d, skipping", webhook.Path, len(result))
				w.Write([]byte("OK"))
				return
			}

			now := time.Now().UTC()
			hash := concrnt.GetHash(fmt.Appendf(nil, "%s:%d", result, now.UnixNano()))
			var seed [10]byte
			copy(seed[:], hash)
			key := fmt.Sprintf("cckv://%s/concrnt.world/profiles/main/posts/%s", ccid, cdid.New(seed, now))

			doc := concrnt.Document[MessageValue]{
				Kind:      "record",
				Key:       key,
				Schema:    schema,
				Value:     MessageValue{Body: result, ProfileOverride: webhook.ProfileOverride},
				Author:    ccid,
				CreatedAt: now,
			}
			if len(webhook.Timelines) > 0 {
				doc.Distributes = &webhook.Timelines
			}
			docBytes, err := json.Marshal(doc)
			if err != nil {
				log.Printf("%s: failed to marshal document: %v", webhook.Path, err)
				http.Error(w, "commit failed", http.StatusInternalServerError)
				return
			}
			sigBytes, err := concrnt.SignBytes(docBytes, config.Concrnt.PrivateKey)
			if err != nil {
				log.Printf("%s: failed to sign document: %v", webhook.Path, err)
				http.Error(w, "commit failed", http.StatusInternalServerError)
				return
			}
			sig := hex.EncodeToString(sigBytes)
			commitBytes, err := json.Marshal(concrnt.SignedDocument{
				Document: string(docBytes),
				Proof: concrnt.Proof{
					Type:      concrnt.ProofTypeEcrecover,
					Signature: &sig,
				},
			})
			if err != nil {
				log.Printf("%s: failed to marshal commit: %v", webhook.Path, err)
				http.Error(w, "commit failed", http.StatusInternalServerError)
				return
			}

			req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, commitURL, bytes.NewReader(commitBytes))
			if err != nil {
				log.Printf("%s: failed to build commit request: %v", webhook.Path, err)
				http.Error(w, "commit failed", http.StatusInternalServerError)
				return
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := httpClient.Do(req)
			if err != nil {
				log.Printf("%s: commit failed: %v", webhook.Path, err)
				http.Error(w, "commit failed", http.StatusInternalServerError)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
				log.Printf("%s: commit failed: status %d: %s", webhook.Path, resp.StatusCode, respBody)
				http.Error(w, "commit failed", http.StatusInternalServerError)
				return
			}
			log.Printf("%s: posted %s", webhook.Path, key)
			w.Write([]byte("OK"))
		})
		log.Printf("registered webhook %s", webhook.Path)
	}

	log.Printf("webhook-relay listening on %s as %s (commit to %s)", config.Listen, ccid, commitURL)
	log.Fatal(http.ListenAndServe(config.Listen, mux))
}
