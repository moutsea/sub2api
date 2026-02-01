package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// WebSearchService handles web_search tool calls
type WebSearchService struct {
	searxngURL string
	httpClient *http.Client
}

// SearXNGResponse represents SearXNG API response structure
type SearXNGResponse struct {
	Results []SearXNGResult `json:"results"`
}

// SearXNGResult represents a single SearXNG result
type SearXNGResult struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

// WebSearchToolResult represents web_search tool result (Anthropic format)
type WebSearchToolResult struct {
	Type    string               `json:"type"` // "web_search_tool_result"
	Results []WebSearchResultItem `json:"results"`
}

// WebSearchResultItem represents a search result item
type WebSearchResultItem struct {
	URL             string `json:"url"`
	Title           string `json:"title"`
	EncryptedContent string `json:"encrypted_content,omitempty"`
	PageContent     string `json:"page_content,omitempty"`
}

var globalWebSearchService *WebSearchService

// InitWebSearchService initializes WebSearch service
func InitWebSearchService() {
	searxngURL := os.Getenv("SEARXNG_URL")
	if searxngURL == "" {
		log.Println("[WebSearch] SEARXNG_URL not configured, WebSearch feature disabled")
		return
	}

	globalWebSearchService = &WebSearchService{
		searxngURL: searxngURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
	log.Printf("[WebSearch] Service initialized with URL: %s", searxngURL)
}

// GetWebSearchService returns the global WebSearch service
func GetWebSearchService() *WebSearchService {
	return globalWebSearchService
}

// IsWebSearchEnabled checks if WebSearch is enabled
func IsWebSearchEnabled() bool {
	return globalWebSearchService != nil
}

// Search executes a search query
func (s *WebSearchService) Search(ctx context.Context, query string) (*WebSearchToolResult, error) {
	if s == nil {
		return nil, fmt.Errorf("WebSearch service not initialized")
	}

	// Build search URL
	searchURL := fmt.Sprintf("%s/search?q=%s&format=json", s.searxngURL, url.QueryEscape(query))

	log.Printf("[WebSearch] Executing search: query=%s", query)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create search request failed: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "sub2api/1.0")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("search returned error status: %d, body: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read search response failed: %w", err)
	}

	var searxngResp SearXNGResponse
	if err := json.Unmarshal(body, &searxngResp); err != nil {
		return nil, fmt.Errorf("parse search response failed: %w", err)
	}

	// Convert to Anthropic format
	result := &WebSearchToolResult{
		Type:    "web_search_tool_result",
		Results: make([]WebSearchResultItem, 0, len(searxngResp.Results)),
	}

	// Limit to max 10 results
	maxResults := 10
	if len(searxngResp.Results) < maxResults {
		maxResults = len(searxngResp.Results)
	}

	for i := 0; i < maxResults; i++ {
		r := searxngResp.Results[i]
		result.Results = append(result.Results, WebSearchResultItem{
			URL:         r.URL,
			Title:       r.Title,
			PageContent: r.Content,
		})
	}

	log.Printf("[WebSearch] Search completed: query=%s results=%d", query, len(result.Results))

	return result, nil
}

// IsWebSearchTool checks if the tool is a web_search tool
func IsWebSearchTool(toolName string) bool {
	// Normalize to lowercase for comparison
	lowerName := strings.ToLower(toolName)
	return lowerName == "web_search" ||
		lowerName == "websearch" ||
		strings.HasPrefix(lowerName, "web_search_") ||
		strings.HasPrefix(lowerName, "websearch_")
}

// FormatSearchResultsForModel formats search results for model consumption
func FormatSearchResultsForModel(result *WebSearchToolResult) string {
	if result == nil || len(result.Results) == 0 {
		return "No search results found."
	}

	var sb strings.Builder
	sb.WriteString("Search Results:\n\n")

	for i, r := range result.Results {
		sb.WriteString(fmt.Sprintf("%d. **%s**\n", i+1, r.Title))
		sb.WriteString(fmt.Sprintf("   URL: %s\n", r.URL))
		if r.PageContent != "" {
			// Truncate overly long content
			content := r.PageContent
			if len(content) > 500 {
				content = content[:500] + "..."
			}
			sb.WriteString(fmt.Sprintf("   %s\n", content))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}
