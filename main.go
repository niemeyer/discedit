package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var (
	debug = flag.Bool("debug", false, "Debug mode")

	ignoreDraft = flag.Bool("ignore-draft", false, "Ignore existing draft and start over")
	forceDraft  = flag.Bool("force-draft", false, "Open draft even if it has conflicts")
	liveEdit    = flag.Bool("live-edit", false, "Update post while content is being edited")
)

type Config struct {
	Forums map[string]*ForumConfig `json:"forums"`
}

type ForumConfig struct {
	Username string `yaml:"username"`
	Key      string `yaml:"key"`
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: discedit <forum topic URL>\n\nOptions:\n\n")
		flag.PrintDefaults()
	}
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

var configPath = "$HOME/.discedit"
var configErr error

func init() {
	configPath = os.ExpandEnv(configPath)

	configErr = fmt.Errorf("%s must contain forums in the YAML format:\n" +
		"forums:\n" +
		"    https://some.discourse.domain:\n" +
		"        username: your-username\n" +
		"        key: your-key\n", configPath)
}

func readConfig() (*Config, error) {
	var config Config

	data, err := ioutil.ReadFile(configPath)
	if os.IsNotExist(err) {
		return nil, configErr
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %v", configPath, err)
	}
	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return nil, fmt.Errorf("cannot unmarshal %s: %v", configPath, err)
	}
	if len(config.Forums) == 0 {
		return nil, configErr
	}

	for baseURL, fconfig := range config.Forums {
		cleanURL := strings.TrimRight(baseURL, "/")
		_, _, err = parseTopicURL(cleanURL + "/t/123")
		if err != nil {
			return nil, fmt.Errorf("%s has invalid forum URL: %q", configPath, baseURL)
		}
		if cleanURL != baseURL {
			config.Forums[cleanURL] = fconfig
			delete(config.Forums, baseURL)
		}
		if fconfig.Username == "" || fconfig.Key == "" {
			return nil, fmt.Errorf("%s misses username or key for forum %s", configPath, baseURL)
		}
	}
	return &config, nil
}

func run() error {
	flag.Parse()

	args := flag.Args()

	if len(args) != 1 {
		flag.Usage()
		os.Exit(1)
	}

	config, err := readConfig()
	if err != nil {
		return err
	}

	baseURL, topicID, err := parseTopicURL(args[0])
	if err != nil {
		return err
	}

	fconfig := config.Forums[baseURL]
	if fconfig == nil {
		return fmt.Errorf("%s misses username and key for forum %s", configPath, baseURL)
	}

	forum := &Forum{
		config:  fconfig,
		baseURL: baseURL,
	}

	topic, err := forum.LoadTopic(topicID)
	if err != nil {
		return err
	}

	if !*ignoreDraft {
		err = forum.LoadDraft(topic)
		if err != nil && !isNotFound(err) {
			return err
		}
		err = topic.CheckDraft()
		if err != nil {
			if *forceDraft {
				logf("Previous draft has problems: %s", err)
				logf("Using draft anyway due to -force-draft")
			} else {
				return err
			}
		}
	}

	var initial = topic.OriginalText()

	var different, empty bool
	filename, err := edit(forum, topic)
	if err == nil {
		// Make sure to check OriginalText after editing, as changes
		// made may have been previously saved via live editing.
		different, empty, err = fileChanged(filename, topic.OriginalText())
	}
	if filename != "" && different && !empty {
		defer renameToLast(filename)
	}
	if err != nil {
		return err
	}
	if empty {
		os.Remove(filename)
		return fmt.Errorf("no content provided, aborting")
	}
	if !different {
		if *liveEdit && initial != topic.OriginalText() {
			logf("Changes already saved.")
		} else {
			logf("No changes to save.")
		}
		os.Remove(filename)
		return nil
	}

	err = forum.SaveTopic(topic, filename)
	if err != nil {
		return err
	}

	return nil
}

func renameToLast(filename string) {
	renameErr := os.Rename(filename, configPath + ".last.md")
	if renameErr != nil {
		logf("WARNING: Cannot save backup: %v", renameErr)
	} else {
		logf("Saved backup: " + configPath + ".last.md")
	}
}

func edit(forum *Forum, topic *Topic) (filename string, err error) {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "sensible-editor"
	}

	logf("Opening your preferred editor...")

	text := topic.EditText()

	tmpfile, err := os.Create(configPath + "." + strconv.Itoa(os.Getpid()) + ".md")
	if err == nil {
		_, err = tmpfile.Write([]byte(text))
	}
	if err == nil {
		err = tmpfile.Close()
	}
	if err != nil {
		if tmpfile != nil {
			tmpfile.Close()
			os.Remove(tmpfile.Name())
		}
		return "", fmt.Errorf("cannot write temporary file: %v", err)
	}
	filename = tmpfile.Name()

	cmd := exec.Command(editor, filename)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	stat, err := os.Stat(filename)
	if err != nil {
		return filename, fmt.Errorf("cannot stat temporary file: %v", err)
	}
	stop := make(chan bool)
	done := make(chan bool)

	go func() {
		defer close(done)
		last := false
		for !last {
			select {
			case <-time.After(500 * time.Millisecond):
			case <-stop:
				last = true
			}

			curstat, err := os.Stat(filename)
			if err != nil {
				debugf("Error stating file for draft: %v", err)
				continue
			}
			if curstat.ModTime() == stat.ModTime() {
				continue
			}
			different, empty, err := fileChanged(filename, text)
			if err != nil || !different || empty {
				continue
			}
			if *liveEdit {
				err = forum.SaveTopic(topic, filename)
				if err != nil {
					debugf("Error saving live edit: %v", err)
					// Try to save the draft at least.
				}
			}
			if !*liveEdit || err != nil {
				err = forum.SaveDraft(topic, filename)
				if err != nil {
					debugf("Error saving draft: %v", err)
					continue
				}
			}
			stat = curstat
			text = topic.EditText()
		}
	}()

	quietMode = true
	err = cmd.Run()
	quietMode = false
	if err != nil {
		return filename, fmt.Errorf("cannot edit file %s: %v", filename, err)
	}

	close(stop)
	<-done

	return filename, nil
}

func fileChanged(filename, original string) (different, empty bool, err error) {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return false, false, fmt.Errorf("cannot tell whether %s changed: %v", filename, err)
	}
	trimmed := string(bytes.TrimSpace(data))
	different = trimmed != strings.TrimSpace(original)
	empty = len(trimmed) == 0
	return different, empty, nil
}

func outputErr(output []byte, err error) error {
	output = bytes.TrimSpace(output)
	if len(output) > 0 {
		if bytes.Contains(output, []byte{'\n'}) {
			err = fmt.Errorf("\n-----\n%s\n-----", output)
		} else {
			err = fmt.Errorf("%s", output)
		}
	}
	return err
}

var topicURLPattern = regexp.MustCompile("^(https?://[^/]+)?(?:/t)?(?:/([a-z0-9-]+))?/([0-9]+)(?:/[0-9]+)?$")

func parseTopicURL(topicURL string) (baseURL string, ID int, err error) {
	m := topicURLPattern.FindStringSubmatch(topicURL)
	if m == nil {
		return "", 0, fmt.Errorf("unsupported topic URL: %q", topicURL)
	}
	id, err := strconv.Atoi(m[3])
	if err != nil {
		return "", 0, fmt.Errorf("internal error: URL pattern matched with non-int page ID")
	}
	return m[1], id, nil
}

type Topic struct {
	ID            int       `json:"id"`
	Slug          string    `json:"slug"`
	Title         string    `json:"title"`
	Category      int       `json:"category_id"`
	BumpedAt      time.Time `json:"bumped_at"`
	DraftKey      string    `json:"draft_key"`
	DraftSequence int       `json:"draft_sequence"`

	Post    *Post
	Draft   *Draft
	content []byte
}

func (t *Topic) EditText() string {
	if t.Draft != nil {
		return t.Draft.EditText()
	}
	return t.Post.EditText()
}

func (t *Topic) OriginalText() string {
	if t.Draft != nil {
		return t.Draft.OriginalText()
	}
	return t.Post.OriginalText()
}

func (t *Topic) CheckDraft() error {
	if t.Draft != nil && t.Draft.OriginalText() != t.Post.OriginalText() {
		return fmt.Errorf("content was changed after existing draft started (see -ignore-draft and -force-draft)")
	}
	return nil
}

func (t *Topic) String() string {
	return fmt.Sprintf("/%s/%d", t.Slug, t.ID)
}

func (t *Topic) ForumURL(forum *Forum) string {
	return fmt.Sprintf("%s/t/%s/%d", forum.baseURL, t.Slug, t.ID)
}

func (t *Topic) LastUpdate() time.Time {
	if t.Post == nil || t.Post.UpdatedAt.IsZero() {
		// Search results do not include updated_at. That's the next best thing.
		return t.BumpedAt
	}
	return t.Post.UpdatedAt
}

func (t *Topic) Blurb() string {
	if t.Post != nil {
		return t.Post.Blurb
	}
	return ""
}

type Draft struct {
	Key      string     `json:"draft_key"`
	TopicID  int        `json:"topic_id"`
	Sequence int        `json:"sequence"`
	Data     *DraftData `json:"data"`
}

func (d *Draft) EditText() string {
	return d.Data.Reply
}

func (d *Draft) OriginalText() string {
	return d.Data.OriginalText
}

type DraftData struct {
	Action       string `json:"action"`
	Title        string `json:"title"`
	Reply        string `json:"reply"`
	OriginalText string `json:"originalText"`
	ComposerTime int    `json:"composerTime"`
	TypingTime   int    `json:"typingTime"`
	PostID       int    `json:"postId"`
	Whisper      bool   `json:"whisper"`
}

type draftData DraftData

func (dd *DraftData) MarshalJSON() ([]byte, error) {
	raw, err := json.Marshal((*draftData)(dd))
	if err != nil {
		return nil, err
	}
	return json.Marshal(string(raw))
}

func (dd *DraftData) UnmarshalJSON(data []byte) error {
	var raw string
	err := json.Unmarshal(data, &raw)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(raw), (*draftData)(dd))
}

type Post struct {
	ID            int       `json:"id"`
	Username      string    `json:"username"`
	Cooked        string    `json:"cooked"`
	Raw           string    `json:"raw"`
	UpdatedAt     time.Time `json:"updated_at"`
	TopicID       int       `json:"topic_id"`
	Blurb         string    `json:"blurb"`
	DraftSequence int       `json:"draft_sequence"`
}

func (p *Post) EditText() string {
	return p.Raw
}

func (p *Post) OriginalText() string {
	return p.Raw
}

type Forum struct {
	config  *ForumConfig
	baseURL string
}

var httpClient = &http.Client{
	Timeout: 10 * time.Second,
}

func (f *Forum) LoadTopic(topicID int) (topic *Topic, err error) {

	logf("Loading topic %d...", topicID)

	var result struct {
		*Topic
		PostStream struct {
			Posts []*Post
		} `json:"post_stream"`
	}

	err = f.do("GET", "/t/"+strconv.Itoa(topicID)+".json?include_raw=true", nil, &result)
	if err != nil {
		return nil, err
	}
	if result.Topic == nil || len(result.PostStream.Posts) == 0 {
		return nil, fmt.Errorf("internal error: topic %d has no posts!?", topicID)
	}

	result.Topic.Post = result.PostStream.Posts[0]
	return result.Topic, nil
}

func (f *Forum) SaveTopic(topic *Topic, filename string) error {
	content, err := ioutil.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("cannot read edited content at %s: %v", filename, err)
	}

	logf("Saving topic %s ...", topic)

	// Discourse drops spaces, so if we don't do this here the value of post.Raw
	// at the end of the function gets out of sync with what's stored server side.
	raw := string(bytes.TrimSpace(content))

	body := map[string]interface{}{
		"post": map[string]interface{}{
			"raw":     raw,
			"raw_old": topic.OriginalText(),
		},
	}

	var result struct {
		Post *Post `json:"post"`
	}
	err = f.do("PUT", "/posts/"+strconv.Itoa(topic.Post.ID)+".json", body, &result)
	if err != nil {
		return err
	}

	logf("Saved %s.", topic)

	topic.Post = result.Post
	topic.Post.Raw = raw
	topic.Draft = nil
	topic.DraftSequence = topic.Post.DraftSequence

	return nil
}

func (f *Forum) LoadDraft(topic *Topic) error {

	logf("Loading draft for topic %d...", topic.ID)

	var result struct {
		Data     *DraftData `json:"draft"`
		Sequence int        `json:"draft_sequence"`
	}
	key := "topic_" + strconv.Itoa(topic.ID)
	err := f.do("GET", "/draft.json?draft_key="+key, nil, &result)
	if err != nil {
		return err
	}

	topic.DraftSequence = result.Sequence
	if result.Data != nil {
		topic.Draft = &Draft{
			Key:      key,
			Sequence: result.Sequence,
			TopicID:  topic.ID,
			Data:     result.Data,
		}
	}
	return nil
}

func (f *Forum) SaveDraft(topic *Topic, filename string) error {
	content, err := ioutil.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("cannot read edited content at %s: %v", filename, err)
	}

	logf("Saving draft for %s ...", topic)

	draft := &Draft{
		Key:      fmt.Sprintf("topic_%d", topic.ID),
		TopicID:  topic.ID,
		Sequence: topic.DraftSequence,
		Data: &DraftData{
			Reply:        string(content),
			Action:       "edit",
			Title:        topic.Title,
			ComposerTime: 4321,
			TypingTime:   1234,
			PostID:       topic.Post.ID,
			OriginalText: topic.OriginalText(),
			Whisper:      false, // What's this?
		},
	}

	var result struct {
		Success       string `json:"success"`
		DraftSequence int    `json:"draft_sequence"`
		ConflictUser  struct {
			ID             int    `json:"id"`
			Username       string `json:"username"`
			Name           string `json:"name"`
			AvatarTemplate string `json:"avatar_template"`
		} `json:"conflict_user"`
	}

	err = f.do("POST", "/draft.json", draft, &result)
	if err != nil {
		return err
	}

	var msg = result.Success
	if msg != "OK" {
		if msg == "" {
			msg = "unknown error"
		}
		return fmt.Errorf("cannot update draft: %q", msg)
	}

	topic.Draft = draft
	topic.DraftSequence = result.DraftSequence

	logf("Saved draft for %s.", topic)
	return nil

}

func (f *Forum) do(verb, path string, body, result interface{}) error {
	var rbody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("internal error: cannot marshal request body: %v", err)
		}
		rbody = bytes.NewReader(data)
		debugf("%s on %s with %s", verb, path, data)
	} else {
		debugf("%s on %s", verb, path)
	}
	req, err := http.NewRequest(verb, f.baseURL+path, rbody)
	if err != nil {
		return fmt.Errorf("cannot create request: %v", err)
	}
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("API-Username", f.config.Username)
	req.Header.Add("API-Key", f.config.Key)
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("cannot perform request on %s: %v", path, err)
	}
	defer resp.Body.Close()

	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("cannot read response (status %d): %v", resp.StatusCode, err)
	}

	debugf("Got response %d with %s", resp.StatusCode, data)

	switch resp.StatusCode {
	case 200:
		// ok
	case 401, 404:
		return &NotFoundError{fmt.Sprintf("resource not found: %s", path)}
	case 409:
		return fmt.Errorf("someone else edited the same content meanwhile")
	default:
		msg := fmt.Sprintf("got %v status", resp.StatusCode)

		var result struct {
			Errors    []string `json:"errors"`
			ErrorType string   `json:"error_type"`
		}
		err = json.Unmarshal(data, &result)
		if err == nil && len(result.Errors) > 0 {
			msg = result.Errors[0]
		} else {
			msg = fmt.Sprintf("got %d status", resp.StatusCode)
		}
		return fmt.Errorf("cannot perform request: %s", msg)
	}

	if result != nil {
		err = json.Unmarshal(data, result)
		if err != nil {
			return fmt.Errorf("cannot decode response from %s: %v", path, err)
		}
	}
	return nil

}

type NotFoundError struct {
	Message string
}

func (e *NotFoundError) Error() string {
	return e.Message
}

func isNotFound(err error) bool {
	_, ok := err.(*NotFoundError)
	return ok
}

var quietMode = false

func logf(format string, args ...interface{}) {
	if !quietMode {
		log.Printf(format, args...)
	}
}

func debugf(format string, args ...interface{}) {
	if *debug {
		log.Printf("[DEBUG] "+format, args...)
	}
}
