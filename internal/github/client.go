package github

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/remote"
)

// Client is a GitHub API client (Cloud or Enterprise).
type Client struct {
	apiBase    string
	token      string
	httpClient *http.Client
}

// NewClient creates a GitHub API client. apiBase should be the API root
// (e.g. https://api.github.com). Trailing slashes are trimmed.
func NewClient(apiBase, token string) *Client {
	return &Client{
		apiBase: strings.TrimSuffix(apiBase, "/"),
		token:   token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// ListOrgRepos lists all repositories in a GitHub organization.
func (c *Client) ListOrgRepos(orgName string) ([]remote.Repository, error) {
	var all []remote.Repository
	page := 1
	perPage := 100

	for {
		endpoint := fmt.Sprintf("%s/orgs/%s/repos?per_page=%d&page=%d&type=all", c.apiBase, url.PathEscape(orgName), perPage, page)

		req, err := http.NewRequest("GET", endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}
		c.addHeaders(req)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetching repos: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
		}

		var repos []struct {
			ID            int64  `json:"id"`
			Name          string `json:"name"`
			FullName      string `json:"full_name"`
			Description   string `json:"description"`
			CloneURL      string `json:"clone_url"`
			SSHURL        string `json:"ssh_url"`
			HTMLURL       string `json:"html_url"`
			DefaultBranch string `json:"default_branch"`
			Archived      bool   `json:"archived"`
			Private       bool   `json:"private"`
			Fork          bool   `json:"fork"`
			Size          int64  `json:"size"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&repos); err != nil {
			return nil, fmt.Errorf("decoding response: %w", err)
		}

		if len(repos) == 0 {
			break
		}

		for _, r := range repos {
			all = append(all, remote.Repository{
				ID:            r.ID,
				Name:          r.Name,
				FullName:      r.FullName,
				Description:   r.Description,
				CloneURL:      r.CloneURL,
				SSHURL:        r.SSHURL,
				HTMLURL:       r.HTMLURL,
				DefaultBranch: r.DefaultBranch,
				Archived:      r.Archived,
				Private:       r.Private,
				Fork:          r.Fork,
				Empty:         r.Size == 0,
			})
		}

		if len(repos) < perPage {
			break
		}
		page++
	}

	return all, nil
}

// GetRepo fetches a single repository by owner/name.
func (c *Client) GetRepo(owner, repoName string) (*remote.Repository, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/%s", c.apiBase, url.PathEscape(owner), url.PathEscape(repoName))

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	c.addHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching repo: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	var r struct {
		ID            int64  `json:"id"`
		Name          string `json:"name"`
		FullName      string `json:"full_name"`
		Description   string `json:"description"`
		CloneURL      string `json:"clone_url"`
		SSHURL        string `json:"ssh_url"`
		HTMLURL       string `json:"html_url"`
		DefaultBranch string `json:"default_branch"`
		Archived      bool   `json:"archived"`
		Private       bool   `json:"private"`
		Fork          bool   `json:"fork"`
		Size          int64  `json:"size"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	repo := &remote.Repository{
		ID:            r.ID,
		Name:          r.Name,
		FullName:      r.FullName,
		Description:   r.Description,
		CloneURL:      r.CloneURL,
		SSHURL:        r.SSHURL,
		HTMLURL:       r.HTMLURL,
		DefaultBranch: r.DefaultBranch,
		Archived:      r.Archived,
		Private:       r.Private,
		Fork:          r.Fork,
		Empty:         r.Size == 0,
	}

	return repo, nil
}

func (c *Client) addHeaders(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "token "+c.token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
}
