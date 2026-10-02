package cron

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Job is one scheduled prompt, as its file holds it.
type Job struct {
	Name string `json:"name"`
	// Schedule is a cron expression, a shorthand, or "@every <duration>".
	// It is empty for a one-shot job, which has At instead.
	Schedule string    `json:"schedule,omitempty"`
	At       time.Time `json:"at,omitzero"`
	Prompt   string    `json:"prompt"`
	// CWD is where the job's session works: where kon was when it added
	// the job.
	CWD     string    `json:"cwd"`
	Created time.Time `json:"created"`
}

// key fingerprints what decides the job's runs. Times decoded from JSON
// carry a fresh location each read, so Job values do not compare equal even
// when nothing changed.
func (j Job) key() string {
	return fmt.Sprintf("%s\x00%s\x00%d\x00%s\x00%s", j.Name, j.Schedule, j.At.UnixNano(), j.Prompt, j.CWD)
}

// When is the job's schedule.
func (j Job) When() (Schedule, error) {
	if j.Schedule == "" {
		if j.At.IsZero() {
			return Schedule{}, errors.New("job has neither a schedule nor a time")
		}
		return At(j.At), nil
	}
	return ParseSchedule(j.Schedule)
}

// validName keeps a job's name usable as its file name everywhere and short
// enough to read in a notice.
var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func (j Job) validate() error {
	if !validName.MatchString(j.Name) {
		return fmt.Errorf("job name %q must be 1-64 lowercase letters, digits, - or _, starting with a letter or digit", j.Name)
	}
	if strings.TrimSpace(j.Prompt) == "" {
		return errors.New("a job needs a prompt")
	}
	if !filepath.IsAbs(j.CWD) {
		return fmt.Errorf("job directory %q is not absolute", j.CWD)
	}
	if j.Schedule != "" && !j.At.IsZero() {
		return errors.New("a job has a schedule or a time, not both")
	}
	_, err := j.When()
	return err
}

// Dir is the directory of job files. Each job is a file of its own, so
// separate `inari cron` runs and the scheduler never write the same file.
type Dir string

func (d Dir) path(name string) string { return filepath.Join(string(d), name+".json") }

// Add writes j. A job of the same name is an error unless replace is set.
// The file appears whole or not at all, so the scheduler never reads half of
// one.
func (d Dir) Add(j Job, replace bool) error {
	if err := j.validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(string(d), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(string(d), ".add-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(append(data, '\n'))
	if err = errors.Join(err, tmp.Close()); err != nil {
		return err
	}
	if replace {
		return os.Rename(tmp.Name(), d.path(j.Name))
	}
	// A link fails when the name is taken, which a rename would not.
	if err := os.Link(tmp.Name(), d.path(j.Name)); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("a job named %q exists; remove it first or pass --replace", j.Name)
		}
		return err
	}
	return nil
}

// Remove deletes the job named name.
func (d Dir) Remove(name string) error {
	err := os.Remove(d.path(name))
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("no job named %q", name)
	}
	return err
}

// List reads every job, sorted by name. A file that does not read as a job
// is reported in errs and skipped, so one bad file stops nothing else.
func (d Dir) List() (jobs []Job, errs []error) {
	entries, err := os.ReadDir(string(d))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, []error{err}
	}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(string(d), e.Name()))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		var j Job
		if err := json.Unmarshal(data, &j); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.Name(), err))
			continue
		}
		if j.Name != name {
			errs = append(errs, fmt.Errorf("%s: names job %q", e.Name(), j.Name))
			continue
		}
		if err := j.validate(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.Name(), err))
			continue
		}
		jobs = append(jobs, j)
	}
	sort.Slice(jobs, func(a, b int) bool { return jobs[a].Name < jobs[b].Name })
	return jobs, errs
}
