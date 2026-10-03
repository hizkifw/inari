package hub

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// attach saves a message's attachments as files under its session's
// directory and returns text that lists their paths, so the model reads each
// with its tools as it would any file. kon copies what it reads into the
// session, so the files only need to last while inari runs and the session
// is in use.
func (h *Hub) attach(sessionID string, attachments []Attachment) (string, error) {
	if len(attachments) == 0 {
		return "", nil
	}
	root, err := h.attachRoot()
	if err != nil {
		return "", err
	}
	session := filepath.Join(root, attachmentName(sessionID))
	if err := os.MkdirAll(session, 0o700); err != nil {
		return "", fmt.Errorf("save attachments: %w", err)
	}
	// Each message gets a directory, so two files with one name never meet.
	dir, err := os.MkdirTemp(session, "")
	if err != nil {
		return "", fmt.Errorf("save attachments: %w", err)
	}
	var b strings.Builder
	b.WriteString("\n\n[attachments]")
	for _, a := range attachments {
		p, err := saveAttachment(dir, a)
		if err != nil {
			return "", fmt.Errorf("save %s: %w", a.Name, err)
		}
		about := sizeText(len(a.Data))
		if a.MIME != "" {
			about = a.MIME + ", " + about
		}
		fmt.Fprintf(&b, "\n- %s (%s)", p, about)
	}
	return b.String(), nil
}

// attachRoot returns the directory attachments are saved under, creating it
// on first use. Close removes it.
func (h *Hub) attachRoot() (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.attachments != "" {
		return h.attachments, nil
	}
	dir, err := os.MkdirTemp("", "inari-attachments-")
	if err != nil {
		return "", fmt.Errorf("create attachment directory: %w", err)
	}
	h.attachments = dir
	return dir, nil
}

// dropAttachments removes a session's saved attachments once nothing will
// read them, such as when /new forgets the session.
func (h *Hub) dropAttachments(sessionID string) {
	h.mu.Lock()
	root := h.attachments
	h.mu.Unlock()
	if root == "" || sessionID == "" {
		return
	}
	dir := filepath.Join(root, attachmentName(sessionID))
	if err := os.RemoveAll(dir); err != nil {
		h.log.Warn("remove attachments", "dir", dir, "err", err)
	}
}

// saveAttachment writes a to dir under its own name, numbering it when a
// message carries two of the same name.
func saveAttachment(dir string, a Attachment) (string, error) {
	name := attachmentName(a.Name)
	for i := 1; ; i++ {
		p := filepath.Join(dir, name)
		if i > 1 {
			p = filepath.Join(dir, fmt.Sprintf("%d-%s", i, name))
		}
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, err = f.Write(a.Data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return p, err
	}
}

// attachmentName keeps the last element of a sender's file name, so a name
// such as "../x" cannot write outside the message's directory.
func attachmentName(name string) string {
	name = path.Base(strings.ReplaceAll(name, `\`, "/"))
	if name == "." || name == ".." || name == "/" {
		return "attachment"
	}
	return name
}

func sizeText(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}
