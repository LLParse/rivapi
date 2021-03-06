package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"regexp"
	"sync"
	"time"

	log "github.com/Sirupsen/logrus"
)

type Token struct {
	Token       string    `json:"token"`
	AccessToken string    `json:"access_token"`
	ExpiresIn   int       `json:"expires_in"`
	IssuedAt    time.Time `json:"issued_at"`
}

type TagList struct {
	Name string   `json="name"`
	Tags []string `json="tags"`
}

func (c *Client) loadTagsPeriodically() {
	c.loadTags()
	go func() {
		t := time.NewTicker(12 * time.Hour)
		for _ = range t.C {
			c.loadTags()
		}
	}()
}

func (c *Client) loadTags() {
	tagHash := make(map[string]string)
	hashTag := make(map[string][]string)

	tagList, _ := c.getTagList()
	var wg sync.WaitGroup
	var m sync.Mutex
	for i, tag := range tagList.Tags {
		wg.Add(1)
		go func(tag string) {
			if hash, err := c.getTagHash(tag); err == nil {
				m.Lock()
				tagHash[tag] = hash
				if _, ok := c.hashTag[hash]; ok {
					hashTag[hash] = append(hashTag[hash], tag)
				} else {
					hashTag[hash] = []string{tag}
				}
				m.Unlock()
			} else {
				log.WithFields(log.Fields{
					"error": err.Error(),
					"tag":   tag,
				}).Warn("Error reading tag")
			}
			wg.Done()
		}(tag)
		// adjust this based on max number of open files `ulimit -n`
		if i%128 == 0 {
			wg.Wait()
		}
	}
	wg.Wait()

	c.Lock()
	c.tagHash = tagHash
	c.hashTag = hashTag
	c.Unlock()
}

func (c *Client) checkToken() error {
	// TODO validate that the token hasn't expired
	if c.token == nil {
		return c.newToken()
	}
	// allow 30 second window for batching/transmission to server
	expiryTime := c.token.IssuedAt.Add(time.Duration(c.token.ExpiresIn-30) * time.Second)
	if time.Now().After(expiryTime) {
		return c.newToken()
	}
	return nil
}

func (c *Client) newToken() error {
	resp, err := http.Get("https://auth.docker.io/token?scope=repository:rancher/server:pull&service=registry.docker.io")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err2 := ioutil.ReadAll(resp.Body)
	if err2 != nil {
		return err2
	}

	var token Token
	err = json.Unmarshal(data, &token)
	if err != nil {
		return err
	}
	c.token = &token
	log.WithFields(log.Fields{
		"expires_in": token.ExpiresIn,
		"issued_at":  token.IssuedAt,
	}).Info("Created auth token")
	return nil
}

func (c *Client) getTagList() (*TagList, error) {
	req := c.authorizedRequest("GET", "https://registry-1.docker.io/v2/rancher/server/tags/list")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err2 := ioutil.ReadAll(resp.Body)
	if err2 != nil {
		return nil, err2
	}

	var tagList TagList
	err = json.Unmarshal(data, &tagList)
	if err != nil {
		return nil, err
	}

	// filter out release candidates
	if !*rc {
		var filtered []string
		for _, tag := range tagList.Tags {
			if matched, err := regexp.MatchString("-rc[0-9]+$", tag); !matched || err != nil {
				filtered = append(filtered, tag)
			}
		}
		tagList.Tags = filtered
	}

	c.tagList = &tagList
	log.WithField("count", len(c.tagList.Tags)).Info("Fetched Rancher version list")

	return &tagList, nil
}

func (c *Client) getTagHash(tag string) (string, error) {
	url := fmt.Sprintf("https://registry-1.docker.io/v2/rancher/server/manifests/%s", tag)
	req := c.authorizedRequest("HEAD", url)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	return resp.Header.Get("Docker-Content-Digest"), nil
}

func (c *Client) authorizedRequest(method, url string) *http.Request {
	req, _ := http.NewRequest(method, url, nil)
	c.checkToken()
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", c.token.AccessToken))
	req.Header.Add("Accept", "application/json")
	req.Header.Add("Accept", "application/vnd.docker.distribution.manifest.v2+json")
	return req
}
