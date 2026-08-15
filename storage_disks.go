package steward

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
)

// Disk is one named place files are stored. Naming several lets an upload go
// where it belongs — a public disk for images a website embeds, a private one
// for documents only the panel should hand out.
type Disk struct {
	// Storage is the backend. A LocalStorage left without a Dir is filled in
	// from the disk's name under Config.UploadDir.
	Storage Storage

	// Public serves this disk's files to anyone who asks, and its URLs are
	// plain and permanent. A private disk — the default — is served only to a
	// session or a signed URL, and StorageURL signs for it.
	Public bool
}

// DefaultDiskName is the disk an upload goes to when nothing names one.
const DefaultDiskName = "local"

// diskNamePattern keeps a name usable as one path segment.
func validDiskName(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// buildDisks resolves Config.Storage and Config.Disks into one table. Config's
// single Storage stays supported: it becomes the default disk, so a panel that
// never heard of disks keeps working.
func buildDisks(cfg *Config) (map[string]Disk, error) {
	out := make(map[string]Disk, len(cfg.Disks)+1)
	if cfg.DefaultDisk == "" {
		if len(cfg.Disks) > 0 {
			// Defaulting to "local" here would invent a disk nobody asked for
			// and quietly send every unmarked upload to it.
			return nil, fmt.Errorf(
				"steward: DefaultDisk must name one of Disks (%s)", strings.Join(diskNames(cfg.Disks), ", "))
		}
		cfg.DefaultDisk = DefaultDiskName
	}
	if len(cfg.Disks) > 0 {
		if _, ok := cfg.Disks[cfg.DefaultDisk]; !ok {
			return nil, fmt.Errorf(
				"steward: DefaultDisk %q is not among Disks (%s)",
				cfg.DefaultDisk, strings.Join(diskNames(cfg.Disks), ", "))
		}
	}
	if !validDiskName(cfg.DefaultDisk) {
		return nil, fmt.Errorf("steward: disk name %q must be lowercase letters, digits, - or _", cfg.DefaultDisk)
	}

	for name, d := range cfg.Disks {
		if !validDiskName(name) {
			return nil, fmt.Errorf("steward: disk name %q must be lowercase letters, digits, - or _", name)
		}
		if d.Storage == nil {
			d.Storage = localDisk(cfg, name)
		} else if ls, ok := d.Storage.(*LocalStorage); ok {
			fillLocalDisk(cfg, name, ls)
		}
		out[name] = d
	}

	// The default disk, unless Disks already declared one under that name.
	if _, ok := out[cfg.DefaultDisk]; !ok {
		d := Disk{Storage: cfg.Storage, Public: cfg.PublicUploads}
		if d.Storage == nil {
			d.Storage = localDisk(cfg, cfg.DefaultDisk)
		} else if ls, ok := d.Storage.(*LocalStorage); ok {
			fillLocalDisk(cfg, cfg.DefaultDisk, ls)
		}
		out[cfg.DefaultDisk] = d
	}
	// Storage stays pointing at the default disk, so anything reading it
	// directly sees what it always did.
	cfg.Storage = out[cfg.DefaultDisk].Storage
	return out, nil
}

// diskNames lists a configured map's names, sorted, for an error message.
func diskNames(disks map[string]Disk) []string {
	out := make([]string, 0, len(disks))
	for name := range disks {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// localDisk is the backend a disk gets when it names no Storage of its own.
func localDisk(cfg *Config, name string) *LocalStorage {
	ls := &LocalStorage{}
	fillLocalDisk(cfg, name, ls)
	return ls
}

// fillLocalDisk supplies whatever a LocalStorage left blank: its own directory
// under UploadDir, the route that serves it, and the key that signs for it.
func fillLocalDisk(cfg *Config, name string, ls *LocalStorage) {
	if ls.Dir == "" {
		// A panel that declares no disks keeps UploadDir itself, so gaining
		// disks does not mean moving files. Once disks are named, every one of
		// them nests under UploadDir by its own name — including the default,
		// because a rule that exempts one of several is a rule people trip on.
		if len(cfg.Disks) == 0 {
			ls.Dir = cfg.UploadDir
		} else {
			ls.Dir = path.Join(cfg.UploadDir, name)
		}
	}
	if ls.BaseURL == "" {
		ls.BaseURL = cfg.Prefix + "/_uploads/" + name
	}
	if len(ls.SigningKey) == 0 {
		ls.SigningKey = cfg.SecretKey
	}
	ls.name = name
}

// Disk returns a named disk. The second result is false for a name that was
// never configured.
func (a *Admin) Disk(name string) (Disk, bool) {
	if name == "" {
		name = a.cfg.DefaultDisk
	}
	d, ok := a.disks[name]
	return d, ok
}

// DiskNames lists the configured disks, sorted.
func (a *Admin) DiskNames() []string {
	names := make([]string, 0, len(a.disks))
	for name := range a.disks {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// DiskURL turns a stored path into a URL on a named disk. A private disk gets a
// signed, expiring link; a public one gets the plain URL, which is the point of
// calling it public.
func (a *Admin) DiskURL(disk, name string) string {
	if name == "" || absoluteRef(name) {
		return name
	}
	d, ok := a.Disk(disk)
	if !ok {
		return ""
	}
	if !d.Public {
		if signer, ok := d.Storage.(SignedURLStorage); ok {
			if u, err := signer.SignedURL(context.Background(), name, a.signedURLTTL()); err == nil {
				return u
			}
		}
	}
	return d.Storage.URL(name)
}

// diskOf resolves a name to a disk, falling back to the default.
func (a *Admin) diskOf(name string) Disk {
	if d, ok := a.Disk(name); ok {
		return d
	}
	return a.disks[a.cfg.DefaultDisk]
}

// uploadRoutePrefix is where a local disk's files are served from.
func (a *Admin) uploadRoutePrefix(name string) string {
	return a.cfg.Prefix + "/_uploads/" + name + "/"
}

// diskFromUploadPath splits "{prefix}/_uploads/{disk}/{name}" into its parts.
func (a *Admin) diskFromUploadPath(p string) (disk, name string) {
	rest := strings.TrimPrefix(p, a.cfg.Prefix+"/_uploads/")
	if rest == p {
		return "", ""
	}
	disk, name, _ = strings.Cut(rest, "/")
	return disk, name
}
