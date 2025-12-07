package gitea

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/remote"
)

// Repository mirrors the Gitea API response. It stays here for direct use and
// to convert into the provider-agnostic remote.Repository.
type Repository struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Description   string `json:"description"`
	CloneURL      string `json:"clone_url"`
	SSHURL        string `json:"ssh_url"`
	HTMLURL       string `json:"html_url"`
	DefaultBranch string `json:"default_branch"`
	Empty         bool   `json:"empty"`
	Archived      bool   `json:"archived"`
	Private       bool   `json:"private"`
	Fork          bool   `json:"fork"`
}

// Client is a Gitea API client
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// NewClient creates a new Gitea API client
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		token:   token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// ListOrgRepos lists all repositories in an organization
func (c *Client) ListOrgRepos(orgName string) ([]remote.Repository, error) {
	var allRepos []remote.Repository
	page := 1
	limit := 50

	for {
		url := fmt.Sprintf("%s/api/v1/orgs/%s/repos?page=%d&limit=%d", c.baseURL, orgName, page, limit)

		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}

		req.Header.Set("Authorization", "token "+c.token)
		req.Header.Set("Accept", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetching repos: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
		}

		var repos []Repository
		if err := json.NewDecoder(resp.Body).Decode(&repos); err != nil {
			return nil, fmt.Errorf("decoding response: %w", err)
		}

		if len(repos) == 0 {
			break
		}

		for _, r := range repos {
			allRepos = append(allRepos, remote.Repository{
				ID:            r.ID,
				Name:          r.Name,
				FullName:      r.FullName,
				Description:   r.Description,
				CloneURL:      r.CloneURL,
				SSHURL:        r.SSHURL,
				HTMLURL:       r.HTMLURL,
				DefaultBranch: r.DefaultBranch,
				Empty:         r.Empty,
				Archived:      r.Archived,
				Private:       r.Private,
				Fork:          r.Fork,
			})
		}

		if len(repos) < limit {
			break
		}

		page++
	}

	return allRepos, nil
}

// GetRepo gets a specific repository
func (c *Client) GetRepo(owner, repoName string) (*remote.Repository, error) {
	url := fmt.Sprintf("%s/api/v1/repos/%s/%s", c.baseURL, owner, repoName)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Authorization", "token "+c.token)
	req.Header.Set("Accept", "application/json")

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

	var repo Repository
	if err := json.NewDecoder(resp.Body).Decode(&repo); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	return &remote.Repository{
		ID:            repo.ID,
		Name:          repo.Name,
		FullName:      repo.FullName,
		Description:   repo.Description,
		CloneURL:      repo.CloneURL,
		SSHURL:        repo.SSHURL,
		HTMLURL:       repo.HTMLURL,
		DefaultBranch: repo.DefaultBranch,
		Empty:         repo.Empty,
		Archived:      repo.Archived,
		Private:       repo.Private,
		Fork:          repo.Fork,
	}, nil
}
