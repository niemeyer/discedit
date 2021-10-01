package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
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
		fmt.Fprintf(os.Stderr, "Usage: discedit <forum topic URL>\n")
		os.Exit(1)
	}
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

var forumsConfigErr = fmt.Errorf("~/.discedit must contain forums in the YAML format:\n" +
	"forums:\n" +
	"    https://some.discourse.domain:\n" +
	"        username: your-username\n" +
	"        key: your-key\n")

func readConfig() (*Config, error) {
	var config Config

	data, err := ioutil.ReadFile(os.ExpandEnv("$HOME/.discedit"))
	if os.IsNotExist(err) {
		return nil, forumsConfigErr
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read ~/.discedit: %v", err)
	}
	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return nil, fmt.Errorf("cannot unmarshal ~/.discedit: %v", err)
	}
	if len(config.Forums) == 0 {
		return nil, forumsConfigErr
	}

	for baseURL, fconfig := range config.Forums {
		cleanURL := strings.TrimRight(baseURL, "/")
		_, _, err = parseTopicURL(cleanURL + "/t/123")
		if err != nil {
			return nil, fmt.Errorf("~/.discedit has invalid forum URL: %q", baseURL)
		}
		if cleanURL != baseURL {
			config.Forums[cleanURL] = fconfig
			delete(config.Forums, baseURL)
		}
		if fconfig.Username == "" || fconfig.Key == "" {
			return nil, fmt.Errorf("~/.discedit misses username or key for forum %s", baseURL)
		}
	}
	return &config, nil
}

func run() error {
	flag.Parse()

	args := flag.Args()

	if len(args) != 1 {
		flag.Usage()
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
		return fmt.Errorf("~/.discedit misses username and key for forum %s", baseURL)
	}

	forum := Forum{
		config:  fconfig,
		baseURL: baseURL,
	}

	topic, err := forum.Topic(topicID)
	if err != nil {
		return err
	}

	edited, different, err := edit(topic)
	if different || err != nil {
		defer renameToLast(edited)
	}
	if err != nil {
		return err
	}
	if !different {
		log.Printf("Content has not changed. Nothing to do.")
		os.Remove(edited.Name())
		return nil
	}

	err = forum.UpdateTopic(topic, edited)
	if err != nil {
		return err
	}

	return nil
}

const lastEditName = "~/.discedit.last.md"

func renameToLast(edited *os.File) {
	renameErr := os.Rename(edited.Name(), os.ExpandEnv("$HOME/.discedit.last.md"))
	if renameErr != nil {
		log.Printf("WARNING: Cannot save backup: %v", renameErr)
	} else {
		log.Printf("Saved backup: %s", lastEditName)
	}
}

func edit(t *Topic) (edited *os.File, different bool, err error) {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "sensible-editor"
	}

	log.Printf("Opening your preferred editor...")

	tmpfile, err := os.Create(fmt.Sprintf("%s/.discedit.%d.md", os.Getenv("HOME"), os.Getpid()))
	if err == nil {
		_, err = tmpfile.Write([]byte(t.Post.Raw))
	}
	if err == nil {
		err = tmpfile.Close()
	}
	if err != nil {
		if tmpfile != nil {
			tmpfile.Close()
			os.Remove(tmpfile.Name())
		}
		return nil, false, fmt.Errorf("cannot write temporary file: %v", err)
	}

	cmd := exec.Command(editor, tmpfile.Name())
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		return nil, false, fmt.Errorf("cannot edit file %s: %v", tmpfile.Name(), err)
	}

	tmpfile, err = os.Open(tmpfile.Name())
	var data []byte
	if err == nil {
		data, err = ioutil.ReadAll(tmpfile)
	}
	if err == nil {
		_, err = tmpfile.Seek(0, 0)
	}
	if err != nil {
		return nil, false, fmt.Errorf("cannot tell whether %s changed: %v", tmpfile.Name(), err)
	}

	different = strings.TrimSpace(string(data)) != strings.TrimSpace(t.Post.Raw)

	return tmpfile, different, nil
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
	ID       int       `json:"id"`
	Slug     string    `json:"slug"`
	Title    string    `json:"title"`
	Category int       `json:"category_id"`
	BumpedAt time.Time `json:"bumped_at"`

	Post    *Post
	content []byte
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

type Post struct {
	ID        int       `json:"id"`
	Username  string    `json:"username"`
	Cooked    string    `json:"cooked"`
	Raw       string    `json:"raw"`
	UpdatedAt time.Time `json:"updated_at"`
	TopicID   int       `json:"topic_id"`
	Blurb     string    `json:"blurb"`
}

type Forum struct {
	config  *ForumConfig
	baseURL string
	cache   map[int]*topicCache
	mu      sync.Mutex
}

type topicCache struct {
	mu    sync.Mutex
	time  time.Time
	topic *Topic
}

const topicCacheTimeout = 1 * time.Hour
const topicCacheFallback = 7 * 24 * time.Hour

func (f *Forum) ResetCache(topic *Topic) {
	f.mu.Lock()
	delete(f.cache, topic.ID)
	f.mu.Unlock()
}

var httpClient = &http.Client{
	Timeout: 10 * time.Second,
}

func (f *Forum) UpdateTopic(topic *Topic, source *os.File) error {
	content, err := ioutil.ReadAll(source)
	if err != nil {
		return fmt.Errorf("cannot read edited content at %s: %v", source.Name(), err)
	}

	log.Printf("Updating topic %s ...", topic)

	data, err := json.Marshal(map[string]interface{}{
		"post": map[string]interface{}{
			"raw": string(content),
		},
	})
	body := bytes.NewReader(data)
	req, err := http.NewRequest("PUT", f.baseURL+"/posts/"+strconv.Itoa(topic.Post.ID)+".json", body)
	if err != nil {
		return fmt.Errorf("cannot create request: %v", err)
	}
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("API-Username", f.config.Username)
	req.Header.Add("API-Key", f.config.Key)
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("cannot perform update: %v", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case 200:
		// ok
	case 401, 404:
		return fmt.Errorf("topic or post not found")

	default:
		msg := fmt.Sprintf("got %v status", resp.StatusCode)

		data, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			msg = fmt.Sprintf("%s and cannot read response: %v", err)
		} else {
			var result struct {
				Errors    []string `json:"errors"`
				ErrorType string   `json:"error_type"`
			}
			err = json.Unmarshal(data, &result)
			if err == nil && len(result.Errors) > 0 {
				msg = result.Errors[0]
			}
		}

		return fmt.Errorf("cannot perform update (saved at %s): %s", source.Name(), msg)
	}

	log.Printf("Update of %s successful.", topic)

	f.ResetCache(topic)

	return nil

}

func (f *Forum) Topic(topicID int) (topic *Topic, err error) {
	now := time.Now()
	f.mu.Lock()
	if f.cache == nil {
		f.cache = make(map[int]*topicCache)
	}
	cache, ok := f.cache[topicID]
	if !ok {
		cache = &topicCache{}
		f.cache[topicID] = cache
	}
	f.mu.Unlock()

	cache.mu.Lock()
	defer cache.mu.Unlock()

	if cache.time.Add(topicCacheTimeout).After(now) {
		return cache.topic, nil
	}

	defer func() {
		if err != nil {
			if cache.topic != nil && cache.time.Add(topicCacheFallback).After(now) {
				topic = cache.topic
				err = nil
			} else {
				f.mu.Lock()
				delete(f.cache, topicID)
				f.mu.Unlock()
			}
		}
	}()

	log.Printf("Fetching topic %d...", topicID)

	req, err := http.NewRequest("GET", f.baseURL+"/t/"+strconv.Itoa(topicID)+".json?include_raw=true", nil)
	if err != nil {
		return nil, fmt.Errorf("cannot create request: %v", err)
	}
	req.Header.Add("API-Username", f.config.Username)
	req.Header.Add("API-Key", f.config.Key)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot obtain topic: %v", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case 200:
		// ok
	case 401, 404:
		return nil, fmt.Errorf("topic not found")

	default:
		return nil, fmt.Errorf("cannot obtain topic: got %v status", resp.StatusCode)
	}

	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cannot read topic: %v", err)
	}

	var result struct {
		*Topic
		PostStream struct {
			Posts []*Post
		} `json:"post_stream"`
	}
	err = json.Unmarshal(data, &result)
	if err != nil {
		return nil, fmt.Errorf("cannot unmarshal topic: %v", err)
	}

	if result.Topic == nil || len(result.PostStream.Posts) == 0 {
		return nil, fmt.Errorf("internal error: topic has no posts!?", err)
	}

	result.Topic.Post = result.PostStream.Posts[0]

	cache.topic = result.Topic
	cache.time = time.Now()

	return result.Topic, nil
}

func (f *Forum) Search(query string) ([]*Topic, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}

	log.Printf("Fetching search results for: %s", query)

	q := url.Values{
		"include_raw": []string{"true"},
		"q":           []string{"#doc @wiki " + query},
	}.Encode()

	resp, err := httpClient.Get(f.baseURL + "/search.json?" + q)
	if err != nil {
		return nil, fmt.Errorf("cannot obtain search results: %v", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case 200:
		// ok
	default:
		return nil, fmt.Errorf("cannot obtain search results: got %v status", resp.StatusCode)
	}

	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cannot read search results: %v", err)
	}

	var result struct {
		Posts  []*Post
		Topics []*Topic
	}
	err = json.Unmarshal(data, &result)
	if err != nil {
		return nil, fmt.Errorf("cannot unmarshal search results: %v", err)
	}

	topicID := make(map[int]*Topic, len(result.Topics))
	for _, topic := range result.Topics {
		topicID[topic.ID] = topic
	}

	var topics []*Topic
	for _, post := range result.Posts {
		if topic, ok := topicID[post.TopicID]; ok { // && topic.ID != indexPageID {
			topic.Post = post
			topics = append(topics, topic)
		}
	}

	// Take the chance we have the content at hand and replace all cached posts.
	now := time.Now()
	f.mu.Lock()
	if f.cache == nil {
		f.cache = make(map[int]*topicCache)
	}
	for _, topic := range topics {
		f.cache[topic.ID] = &topicCache{
			topic: topic,
			time:  now,
		}
	}
	f.mu.Unlock()

	return topics, nil
}
