// Package tracker provisions and binds project-management projects.
//
// Scope split, and the reason for it: the binary talks to the tracker over
// REST, while the agent does decomposition through an MCP connector. Project
// creation is infrastructure, and infrastructure wants to be deterministic,
// idempotent and verifiable. Asking an agent to create a project gives you
// none of those. Asking an agent to break a plan into stories plays to what
// it is actually good at.
//
// A warning that is built into the code rather than left in a doc: creating
// one project per idea is what this is configured to do, and it accumulates.
// Jira project keys are globally unique per instance, capped at 10 uppercase
// characters, and a non-admin cannot delete a project once made. Key
// collisions across many ideas are certain, not hypothetical, so derivation
// resolves them deterministically rather than failing.
package tracker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"
)

// Binding is what a workspace records about its tracker. Persisted in
// task.json so later stages can reference issue ids in commits and PRs.
type Binding struct {
	Provider  string    `json:"provider"`
	BaseURL   string    `json:"base_url"`
	ProjectID string    `json:"project_id,omitempty"`
	Key       string    `json:"key"`
	Name      string    `json:"name,omitempty"`
	Created   bool      `json:"created"` // false when bound to a pre-existing project
	BoundAt   time.Time `json:"bound_at"`
}

// Capability is what the precheck discovered. Kept separate from Binding so
// doctor can report readiness without touching a workspace.
type Capability struct {
	Reachable        bool
	Authenticated    bool
	AccountID        string
	DisplayName      string
	CanCreateProject bool
	// Undetermined means the permission question could not be answered, as
	// distinct from being answered "no". Collapsing the two would send a
	// user to fix a problem they may not have.
	Undetermined bool
	Detail       string
}

// Tracker is the interface a provider implements. Jira is the only
// implementation today; the interface exists so adding Linear or GitLab is
// a new file rather than a rewrite of the callers.
type Tracker interface {
	Probe() (Capability, error)
	ProjectExists(key string) (bool, string, error)
	CreateProject(key, name, lead string) (Binding, error)
	Name() string
}

// ---------------------------------------------------------------- Jira

type Jira struct {
	BaseURL string
	Email   string
	Token   string
	client  *http.Client
}

// NewJiraFromEnv builds a client from the environment. Credentials are read
// here and nowhere else, and are never written to a log or a task file.
func NewJiraFromEnv() (*Jira, error) {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("ORION_JIRA_URL")), "/")
	email := strings.TrimSpace(os.Getenv("ORION_JIRA_EMAIL"))
	token := strings.TrimSpace(os.Getenv("ORION_JIRA_TOKEN"))

	var missing []string
	if base == "" {
		missing = append(missing, "ORION_JIRA_URL (e.g. https://yourorg.atlassian.net)")
	}
	if email == "" {
		missing = append(missing, "ORION_JIRA_EMAIL")
	}
	if token == "" {
		missing = append(missing, "ORION_JIRA_TOKEN (create at id.atlassian.com/manage-profile/security/api-tokens)")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("Jira is not configured. Missing:\n  %s", strings.Join(missing, "\n  "))
	}
	return &Jira{
		BaseURL: base, Email: email, Token: token,
		client: &http.Client{Timeout: 20 * time.Second},
	}, nil
}

func (j *Jira) Name() string { return "jira" }

func (j *Jira) do(method, path string, body any) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, j.BaseURL+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.SetBasicAuth(j.Email, j.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := j.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, err
}

// Probe answers the questions the precheck needs, before any workspace
// exists. It checks the project-creation permission explicitly rather than
// discovering it by trying: finding out mid-run that you lack the right,
// after an idea has been captured and planned, wastes the whole run.
func (j *Jira) Probe() (Capability, error) {
	cap := Capability{}

	code, body, err := j.do("GET", "/rest/api/3/myself", nil)
	if err != nil {
		cap.Detail = "cannot reach " + j.BaseURL + ": " + err.Error()
		return cap, err
	}
	cap.Reachable = true

	switch {
	case code == 401 || code == 403:
		cap.Detail = "authentication rejected. Check ORION_JIRA_EMAIL and ORION_JIRA_TOKEN; " +
			"the token must belong to that account."
		return cap, nil
	case code >= 400:
		cap.Detail = fmt.Sprintf("unexpected %d from /myself: %s", code, snippet(body))
		return cap, nil
	}

	var me struct {
		AccountID   string `json:"accountId"`
		DisplayName string `json:"displayName"`
	}
	if err := json.Unmarshal(body, &me); err != nil {
		cap.Detail = "could not parse /myself response"
		return cap, nil
	}
	cap.Authenticated = true
	cap.AccountID = me.AccountID
	cap.DisplayName = me.DisplayName

	// Global permission check.
	//
	// The key is DISCOVERED, not hardcoded. Atlassian has renamed this
	// permission ("Create team-managed projects", latterly "Create
	// team-managed spaces"), the key differs between Cloud and Data Center,
	// and the bulk-permissions endpoint returns 400 on a key it does not
	// recognise. Hardcoding one guess means a wrong guess reads as "no
	// permission", which sends the user off to fix a problem they do not
	// have.
	//
	// Note on scope: the team-managed creation permission covers exactly the
	// kind of project CreateProject makes. Company-managed projects need
	// ADMINISTER, which is why ADMINISTER is checked too rather than assumed
	// to be a superset.
	granted, determined := j.globalPermissions(projectCreateKeys)
	if !determined {
		// A probe that cannot answer must say so. Asserting a negative here
		// would be worse than admitting uncertainty: Orion still attempts
		// creation and degrades cleanly if it is refused.
		cap.Detail = "authenticated as " + me.DisplayName +
			", but project-creation permission could not be determined on this instance.\n" +
			"  Orion will attempt creation and fall back to binding an existing key."
		cap.Undetermined = true
		return cap, nil
	}
	cap.CanCreateProject = len(granted) > 0
	if !cap.CanCreateProject {
		cap.Detail = "authenticated as " + me.DisplayName +
			", but this account cannot create Jira projects.\n" +
			"  Either grant it the \"Create team-managed projects\" global permission,\n" +
			"  or set tracker.project_key in orion.json to bind an existing project."
	} else {
		cap.Detail = "authenticated as " + me.DisplayName +
			", can create projects (via " + strings.Join(granted, ", ") + ")"
	}
	return cap, nil
}

// projectCreateKeys are the keys that have meant "may create a project"
// across Jira versions and deployment types. Probed in order; the first the
// instance recognises wins. ADMINISTER and SYSTEM_ADMIN are included because
// they imply the capability even where the narrower key is absent.
var projectCreateKeys = []string{
	"CREATE_PROJECT", // Jira Cloud: "Create team-managed projects"
	"ADMINISTER",     // implies project creation, incl. company-managed
	"SYSTEM_ADMIN",
}

// globalPermissions returns which of the candidate keys are granted, and
// whether the question could be answered at all.
//
// The second return value is the important one. false means "unknown", which
// is a different thing from "none granted" and must not be collapsed into it.
func (j *Jira) globalPermissions(candidates []string) (granted []string, determined bool) {
	// Probe keys individually. A single bulk call fails wholesale if any one
	// key is unrecognised, which would discard the answer for the keys that
	// were valid.
	for _, key := range candidates {
		code, body, err := j.do("POST", "/rest/api/3/permissions/check", map[string]any{
			"globalPermissions": []string{key},
		})
		if err != nil {
			continue
		}
		if code == 400 {
			continue // key not recognised on this instance; try the next
		}
		if code >= 400 {
			continue // endpoint restricted; another key may still answer
		}
		determined = true
		var perms struct {
			GlobalPermissions []string `json:"globalPermissions"`
		}
		if json.Unmarshal(body, &perms) != nil {
			continue
		}
		for _, p := range perms.GlobalPermissions {
			if p == key {
				granted = append(granted, key)
			}
		}
	}
	return granted, determined
}

func (j *Jira) ProjectExists(key string) (bool, string, error) {
	code, body, err := j.do("GET", "/rest/api/3/project/"+key, nil)
	if err != nil {
		return false, "", err
	}
	if code == 404 {
		return false, "", nil
	}
	if code >= 400 {
		return false, "", fmt.Errorf("checking project %s: %d %s", key, code, snippet(body))
	}
	var p struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(body, &p)
	return true, p.ID, nil
}

// CreateProject makes a team-managed software project. The template choice
// matters: a team-managed project can be created by a wider set of accounts
// than a company-managed one, which is the difference between this working
// and not for most users.
func (j *Jira) CreateProject(key, name, leadAccountID string) (Binding, error) {
	payload := map[string]any{
		"key":            key,
		"name":           name,
		"projectTypeKey": "software",
		"projectTemplateKey": "com.pyxis.greenhopper.jira:gh-simplified-agility-scrum",
		"leadAccountId":  leadAccountID,
		"assigneeType":   "PROJECT_LEAD",
		"description":    "Provisioned by Orion.",
	}
	code, body, err := j.do("POST", "/rest/api/3/project", payload)
	if err != nil {
		return Binding{}, err
	}
	if code == 403 {
		return Binding{}, ErrNoPermission
	}
	if code >= 400 {
		return Binding{}, fmt.Errorf("creating project %s: %d %s", key, code, snippet(body))
	}
	var created struct {
		ID  json.Number `json:"id"`
		Key string      `json:"key"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		return Binding{}, err
	}
	return Binding{
		Provider: "jira", BaseURL: j.BaseURL,
		ProjectID: created.ID.String(), Key: created.Key, Name: name,
		Created: true, BoundAt: time.Now().UTC(),
	}, nil
}

// ErrNoPermission is returned when creation is refused for authorization
// reasons, so the caller can degrade to binding rather than abort.
var ErrNoPermission = errors.New("account lacks permission to create a Jira project")

// ---------------------------------------------------------------- keys

// DeriveKey turns a slug into a candidate Jira project key.
//
// Jira keys must start with a letter, contain only uppercase letters and
// digits, and are capped at 10 characters. Rather than take the first ten
// characters of the slug, which collides constantly across similar ideas,
// this takes the initial of each word: "claim-status-self-service" becomes
// CSSS. Short slugs fall back to the leading letters of the first word.
func DeriveKey(slug string) string {
	words := strings.FieldsFunc(strings.ToUpper(slug), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	var b strings.Builder
	for _, w := range words {
		if w == "" {
			continue
		}
		b.WriteString(w[:1])
		if b.Len() >= 6 {
			break
		}
	}
	key := b.String()
	if len(key) < 2 && len(words) > 0 {
		key = words[0]
	}
	// Strip any leading digits: Jira requires a letter first.
	key = strings.TrimLeftFunc(key, func(r rune) bool { return !unicode.IsLetter(r) })
	if key == "" {
		key = "ORION"
	}
	if len(key) > 8 {
		key = key[:8] // leave room for a collision suffix
	}
	return key
}

// ResolveKey finds a free key, appending a numeric suffix on collision.
// Deterministic and bounded: it will not spin looking for a free slot.
func ResolveKey(t Tracker, base string) (string, error) {
	exists, _, err := t.ProjectExists(base)
	if err != nil {
		return "", err
	}
	if !exists {
		return base, nil
	}
	for i := 2; i <= 20; i++ {
		cand := fmt.Sprintf("%s%d", base, i)
		if len(cand) > 10 {
			cand = fmt.Sprintf("%s%d", base[:10-len(fmt.Sprint(i))], i)
		}
		exists, _, err := t.ProjectExists(cand)
		if err != nil {
			return "", err
		}
		if !exists {
			return cand, nil
		}
	}
	return "", fmt.Errorf("could not find a free project key near %q after 20 attempts.\n"+
		"  This usually means many Orion projects have accumulated. Consider binding an\n"+
		"  existing key with tracker.project_key instead of creating another project.", base)
}

// Provision binds a workspace to a tracker project, creating one when
// configured and permitted, and degrading to an existing key when not.
//
// The degradation is the important part. A user without admin rights should
// get working software plus a clear explanation, not a hard failure three
// stages into a run.
func Provision(t Tracker, slug, humanName, existingKey, leadAccountID string) (Binding, string, error) {
	if existingKey != "" {
		exists, id, err := t.ProjectExists(existingKey)
		if err != nil {
			return Binding{}, "", err
		}
		if !exists {
			return Binding{}, "", fmt.Errorf("configured tracker.project_key %q does not exist", existingKey)
		}
		return Binding{
			Provider: t.Name(), ProjectID: id, Key: existingKey,
			Created: false, BoundAt: time.Now().UTC(),
		}, "bound to existing project " + existingKey, nil
	}

	key, err := ResolveKey(t, DeriveKey(slug))
	if err != nil {
		return Binding{}, "", err
	}
	b, err := t.CreateProject(key, humanName, leadAccountID)
	if err != nil {
		if errors.Is(err, ErrNoPermission) {
			return Binding{}, "", fmt.Errorf(
				"cannot create a Jira project: this account lacks the permission.\n"+
					"  Set tracker.project_key in orion.json to an existing project and Orion\n"+
					"  will create its issues there instead. Nothing else changes.")
		}
		return Binding{}, "", err
	}
	return b, "created project " + b.Key, nil
}

func snippet(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > 200 {
		s = s[:199] + "…"
	}
	return s
}
