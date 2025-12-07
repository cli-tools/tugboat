package remote

// Repository is a normalized representation of a source control repository
// independent of the backing service (Gitea, GitHub, etc.).
type Repository struct {
	ID            int64
	Name          string
	FullName      string
	Description   string
	CloneURL      string
	SSHURL        string
	HTMLURL       string
	DefaultBranch string
	Empty         bool
	Archived      bool
	Private       bool
	Fork          bool
}

// GetCloneURL returns the preferred clone URL (SSH when available and requested).
func (r Repository) GetCloneURL(preferSSH bool) string {
	if preferSSH && r.SSHURL != "" {
		return r.SSHURL
	}
	return r.CloneURL
}

// Client defines the minimal operations the repository manager needs from a
// remote provider.
type Client interface {
	ListOrgRepos(orgName string) ([]Repository, error)
	GetRepo(owner, repoName string) (*Repository, error)
}
