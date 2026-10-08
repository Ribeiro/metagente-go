// Package trust keeps what a person has approved for a project (requirements
// T1 and T2).
//
// An agent file can start a program (`tool x from mcp "npx -y something"`) or
// connect to an address. Running a file someone else wrote must not do that
// before the person has seen it. The approvals live in the folder of the user,
// never inside the project, because a project that could change its own
// approvals would not be asking anyone.
package trust

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Ribeiro/metagente-go/internal/diag"
	"github.com/Ribeiro/metagente-go/internal/lang"
)

// saveFailed is the problem for any step of saving the approvals that did not work.
const saveFailed = "I could not save the approvals: %v"

// Kind tells what an item is.
type Kind string

const (
	// KindCommand is a program an agent starts.
	KindCommand Kind = "command"
	// KindRemote is an address an agent connects to.
	KindRemote Kind = "remote"
	// KindModel is an address that is sent the key and the questions of a
	// language model, when it is not the default one of the provider.
	KindModel Kind = "model"
	// KindSQL is a database an agent reads, with the statements it may run on it.
	KindSQL Kind = "sql"
	// KindBroker is a message broker an agent publishes to, with the subjects it may publish to.
	KindBroker Kind = "broker"
)

// Item is one thing that needs approval.
type Item struct {
	Kind   Kind   `json:"kind"`
	Target string `json:"target"`
	// Env are the names of the variables the program receives, sorted.
	Env []string `json:"env,omitempty"`
	// Credential is the NAME of the variable whose token is sent to the address
	// as a bearer token. The same address with another credential is another
	// approval, because the token goes there.
	Credential string `json:"credential,omitempty"`
	// Detail says more about what is approved, and is part of it: for a database, the connection, the
	// statements and a fingerprint of their text, so that a change to a statement is asked again.
	Detail string `json:"detail,omitempty"`
	// Keyless is true for the address of a model to which no key is sent: none is set, and the address is
	// of this same computer. It only changes what Describe says. It is not part of the key of the item and
	// it is not saved, so what was approved before stays approved.
	Keyless bool `json:"-"`
}

// CommandItem is a program started with a command line. The variables passed
// on are part of what is approved: the same program with one more variable is
// not the same thing.
func CommandItem(command string, env []string) Item {
	names := append([]string(nil), env...)
	sort.Strings(names)
	names = dedupe(names)
	return Item{Kind: KindCommand, Target: strings.TrimSpace(command), Env: names}
}

// ModelItem is a non default address of a language model. The key of the person
// is sent there, so it needs approval like a program does (requirement T3).
func ModelItem(address string) Item {
	return Item{Kind: KindModel, Target: strings.TrimSpace(address)}
}

// WithoutKey returns the item of a model that is told to be sent no key.
func (i Item) WithoutKey() Item {
	i.Keyless = true
	return i
}

// SQLItem is a database an agent reads. The connection, the names of the statements and the fingerprint of
// their text are part of what is approved: the same database with another statement is not the same thing.
func SQLItem(driver, target, connection string, statements []string, fingerprint string) Item {
	return Item{
		Kind:   KindSQL,
		Target: strings.TrimSpace(driver + " " + target),
		Detail: fmt.Sprintf("connection %s, statements %s, fingerprint %s", connection, strings.Join(statements, ", "), fingerprint),
	}
}

// BrokerItem is a message broker an agent publishes to. The subjects it may publish to are part of what is
// approved, and the fingerprint covers the settings of the connection.
func BrokerItem(driver, target, connection string, subjects []string, fingerprint string) Item {
	return Item{
		Kind:   KindBroker,
		Target: strings.TrimSpace(driver + " " + target),
		Detail: fmt.Sprintf("connection %s, subjects %s, fingerprint %s", connection, strings.Join(subjects, ", "), fingerprint),
	}
}

// RemoteItem is an address an agent connects to.
func RemoteItem(address string) Item {
	return Item{Kind: KindRemote, Target: strings.TrimSpace(address)}
}

func dedupe(sorted []string) []string {
	if len(sorted) == 0 {
		return nil
	}
	out := sorted[:1]
	for _, s := range sorted[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}

// Key identifies the item. Two items with the same key are the same approval.
func (i Item) Key() string {
	key := string(i.Kind) + "\x00" + i.Target + "\x00" + strings.Join(i.Env, ",")
	if i.Credential != "" {
		// Only added when there is one, so the approvals made before credentials
		// existed keep the same key, and the same hash.
		key += "\x00" + i.Credential
	}
	if i.Detail != "" {
		key += "\x00" + i.Detail
	}
	return key
}

// WithCredential returns the item with the credential that will be sent to it.
func (i Item) WithCredential(variable string) Item {
	i.Credential = variable
	return i
}

// Describe says what the item does, in a sentence for the person to read.
func (i Item) Describe() string {
	switch i.Kind {
	case KindCommand:
		text := "starts the program: " + i.Target
		if len(i.Env) > 0 {
			text += " (it receives the variables " + strings.Join(i.Env, ", ") + ")"
		}
		return text
	case KindModel:
		if i.Keyless {
			return "sends what the agent asks the language model to, with no key (none is set): " + i.Target
		}
		return "sends your key and what the agent asks the language model to: " + i.Target
	case KindSQL:
		return "reads the database: " + i.Target + " (" + i.Detail + ")"
	case KindBroker:
		return "uses the message broker: " + i.Target + " (" + i.Detail + ")"
	default:
		text := "connects to: " + i.Target
		if i.Credential != "" {
			text += " (and sends it the token held in " + i.Credential + ")"
		}
		return text
	}
}

// NeedsOf lists what the agents start or connect to: the tool servers they
// declare and the remote agents they call. The list has no repeats and a stable
// order.
func NeedsOf(agents []*lang.AgentDef) []Item { return NeedsOfWith(agents, nil) }

// NeedsOfWith is NeedsOf for a setup with credentials: the name of a remote agent
// or of a tool server that is an address, as the agent file writes it, to the
// name of the variable whose token is sent to it.
func NeedsOfWith(agents []*lang.AgentDef, credentials map[string]string) []Item {
	seen := map[string]Item{}
	for _, agent := range agents {
		for _, tool := range agent.Tools {
			if tool.Kind != lang.ToolMCP || strings.TrimSpace(tool.Command) == "" {
				continue
			}
			item := CommandItem(tool.Command, tool.MCPEnv)
			if lang.IsURL(tool.Command) {
				// A program is given variables by `env`; only an address gets a token.
				item = RemoteItem(tool.Command).WithCredential(credentials[tool.Name])
			}
			seen[item.Key()] = item
		}
		for _, remote := range agent.Remotes {
			item := RemoteItem(remote.URL).WithCredential(credentials[remote.Name])
			seen[item.Key()] = item
		}
	}
	return sorted(seen)
}

// Merge joins lists of items without repeats.
func Merge(lists ...[]Item) []Item {
	seen := map[string]Item{}
	for _, list := range lists {
		for _, item := range list {
			seen[item.Key()] = item
		}
	}
	return sorted(seen)
}

func sorted(set map[string]Item) []Item {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]Item, len(keys))
	for i, key := range keys {
		out[i] = set[key]
	}
	return out
}

// SetHash is the SHA-256 of a set of items, in hex. It does not depend on the
// order of the items.
func SetHash(items []Item) string {
	keys := make([]string, len(items))
	for i, item := range items {
		keys[i] = item.Key()
	}
	sort.Strings(keys)
	sum := sha256.Sum256([]byte(strings.Join(keys, "\n")))
	return hex.EncodeToString(sum[:])
}

// record is what is stored for one project.
type record struct {
	Items      []Item `json:"items"`
	SHA256     string `json:"sha256"`
	ApprovedAt string `json:"approved_at"`
}

type document struct {
	Version  int               `json:"version"`
	Projects map[string]record `json:"projects"`
}

const fileName = "trust.json"

// Registry is the file of approvals of one user.
type Registry struct {
	dir string
}

// NewRegistry uses the given folder. Nothing is read or written until it is
// needed.
func NewRegistry(dir string) *Registry { return &Registry{dir: dir} }

// DefaultDir is the folder of the approvals of the user: the one named by
// METAGENTE_CONFIG_DIR, or the configuration folder of the system.
func DefaultDir() string {
	if dir := os.Getenv("METAGENTE_CONFIG_DIR"); dir != "" {
		return dir
	}
	if base, err := os.UserConfigDir(); err == nil && base != "" {
		return filepath.Join(base, "metagente")
	}
	return ""
}

// Path is the file of approvals.
func (r *Registry) Path() string { return filepath.Join(r.dir, fileName) }

func canonicalRoot(root string) string {
	abs, err := filepath.Abs(root)
	if err != nil {
		return root
	}
	return resolveDeep(abs)
}

// resolveDeep follows the symbolic links of a path, also when its last parts do
// not exist yet: it resolves the deepest folder that exists and puts the rest
// back. Without that, a folder reached through a link would not be recognised
// as the folder it is, and a check of "is this inside the project" could be
// fooled by the prefix.
func resolveDeep(abs string) string {
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	parent := filepath.Dir(abs)
	if parent == abs {
		return abs
	}
	return filepath.Join(resolveDeep(parent), filepath.Base(abs))
}

// inside reports whether path is the folder root or lies below it.
func inside(root, path string) bool {
	rel, err := filepath.Rel(canonicalRoot(root), canonicalRoot(path))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

func (r *Registry) usable(root string) error {
	if r.dir == "" {
		return diag.New("I could not find a folder of yours to keep the approvals in").
			Fix("set METAGENTE_CONFIG_DIR to a folder outside the project, or make sure your system has a configuration folder")
	}
	if inside(root, r.dir) {
		return diag.New("the approvals would be kept inside the project, where the project could change them").
			Fixf("set METAGENTE_CONFIG_DIR to a folder outside %s", root)
	}
	return nil
}

func (r *Registry) load() (*document, error) {
	empty := &document{Version: 1, Projects: map[string]record{}}
	info, err := os.Stat(r.Path())
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return nil, diag.Newf("I could not read the approvals: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o022 != 0 {
		return nil, diag.New("the file of approvals can be changed by other users of this machine, so I will not trust it").
			Fixf("restrict it with: chmod 600 %s", r.Path())
	}
	raw, err := os.ReadFile(r.Path())
	if err != nil {
		return nil, diag.Newf("I could not read the approvals: %v", err)
	}
	var doc document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, diag.New("the file of approvals is damaged").
			Fixf("remove it and approve again: %s", r.Path())
	}
	if doc.Version != 1 {
		return nil, diag.Newf("the file of approvals has version %d, and this build reads version 1", doc.Version).
			Fix("use the same version of Metagente that wrote it, or remove the file and approve again")
	}
	if doc.Projects == nil {
		doc.Projects = map[string]record{}
	}
	for project, rec := range doc.Projects {
		if rec.SHA256 != SetHash(rec.Items) {
			return nil, diag.Newf("the approvals of %s were changed by hand, so I will not trust them", project).
				Fixf("remove %s and approve again", r.Path())
		}
	}
	return &doc, nil
}

func (r *Registry) save(doc *document) error {
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		return diag.Newf("I could not create the folder of the approvals: %v", err)
	}
	temp, err := os.CreateTemp(r.dir, ".trust-*.tmp")
	if err != nil {
		return diag.Newf(saveFailed, err)
	}
	defer os.Remove(temp.Name()) // does nothing once the rename has happened
	if err := temp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		temp.Close()
		return diag.Newf(saveFailed, err)
	}
	encoder := json.NewEncoder(temp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(doc); err != nil {
		temp.Close()
		return diag.Newf(saveFailed, err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return diag.Newf(saveFailed, err)
	}
	if err := temp.Close(); err != nil {
		return diag.Newf(saveFailed, err)
	}
	if err := os.Rename(temp.Name(), r.Path()); err != nil {
		return diag.Newf(saveFailed, err)
	}
	return nil
}

// Missing returns the items that are not approved for the project. An empty
// result means everything in needs may go ahead.
func (r *Registry) Missing(root string, needs []Item) ([]Item, error) {
	if len(needs) == 0 {
		return nil, nil
	}
	if err := r.usable(root); err != nil {
		return nil, err
	}
	doc, err := r.load()
	if err != nil {
		return nil, err
	}
	approved := map[string]bool{}
	for _, item := range doc.Projects[canonicalRoot(root)].Items {
		approved[item.Key()] = true
	}
	var missing []Item
	for _, item := range needs {
		if !approved[item.Key()] {
			missing = append(missing, item)
		}
	}
	return missing, nil
}

// Allows tells whether one item is approved for the project.
func (r *Registry) Allows(root string, item Item) (bool, error) {
	missing, err := r.Missing(root, []Item{item})
	if err != nil {
		return false, err
	}
	return len(missing) == 0, nil
}

// Approve adds items to what is approved for the project.
func (r *Registry) Approve(root string, items []Item) error {
	if err := r.usable(root); err != nil {
		return err
	}
	doc, err := r.load()
	if err != nil {
		return err
	}
	key := canonicalRoot(root)
	merged := Merge(doc.Projects[key].Items, items)
	doc.Projects[key] = record{
		Items:      merged,
		SHA256:     SetHash(merged),
		ApprovedAt: time.Now().UTC().Format(time.RFC3339),
	}
	return r.save(doc)
}

// Revoke removes every approval of the project.
func (r *Registry) Revoke(root string) (removed int, err error) {
	if err := r.usable(root); err != nil {
		return 0, err
	}
	doc, err := r.load()
	if err != nil {
		return 0, err
	}
	key := canonicalRoot(root)
	rec, ok := doc.Projects[key]
	if !ok {
		return 0, nil
	}
	delete(doc.Projects, key)
	return len(rec.Items), r.save(doc)
}

// Approved is what is approved for one project, with the hash that ties the
// list together.
type Approved struct {
	Project string
	Items   []Item
	SHA256  string
	At      string
}

// List returns the approvals of every project, ordered by project.
func (r *Registry) List() ([]Approved, error) {
	if r.dir == "" {
		return nil, r.usable("")
	}
	doc, err := r.load()
	if err != nil {
		return nil, err
	}
	projects := make([]string, 0, len(doc.Projects))
	for project := range doc.Projects {
		projects = append(projects, project)
	}
	sort.Strings(projects)
	out := make([]Approved, 0, len(projects))
	for _, project := range projects {
		rec := doc.Projects[project]
		out = append(out, Approved{Project: project, Items: rec.Items, SHA256: rec.SHA256, At: rec.ApprovedAt})
	}
	return out, nil
}

// Short is the beginning of a hash, enough for the person to compare.
func Short(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

// Explain renders a list of items as lines for the person to read.
func Explain(items []Item) []string {
	lines := make([]string, len(items))
	for i, item := range items {
		lines[i] = fmt.Sprintf("  %s", item.Describe())
	}
	return lines
}
