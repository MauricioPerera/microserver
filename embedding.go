package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

const embeddingModel = "embeddinggemma"

// ollamaEmbedURL defaults to localhost, which only reaches Ollama when both
// processes run on the same host network. Inside a container "localhost" is
// the container itself, not the Docker host — override with OLLAMA_URL
// there (e.g. http://host.docker.internal:11434/api/embed).
var ollamaEmbedURL = func() string {
	if v := os.Getenv("OLLAMA_URL"); v != "" {
		return v
	}
	return "http://localhost:11434/api/embed"
}()

type embedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

func embed(text string) ([]float32, error) {
	body, err := json.Marshal(embedRequest{Model: embeddingModel, Input: text})
	if err != nil {
		return nil, err
	}
	resp, err := http.Post(ollamaEmbedURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("calling ollama: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama returned status %d", resp.StatusCode)
	}

	var er embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, fmt.Errorf("decoding ollama response: %w", err)
	}
	if len(er.Embeddings) == 0 {
		return nil, fmt.Errorf("ollama returned no embeddings")
	}
	return er.Embeddings[0], nil
}
