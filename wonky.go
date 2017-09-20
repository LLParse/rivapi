package main

import (
	"bufio"
	"fmt"
	"regexp"
	"strings"

	log "github.com/Sirupsen/logrus"
)

// getWonkyImages gets the Docker images that are injected in cattle binaries
func (c *Client) getWonkyImages(rancherVersion string) []string {
	var images []string

	cattleVersion := c.detectCattleVersion(rancherVersion)
	if cattleVersion == "" {
		log.Warn("Couldn't find CATTLE_CATTLE_VERSION in Dockerfile")
		return images
	}
	log.WithFields(log.Fields{
		"cattleVersion":  cattleVersion,
		"rancherVersion": rancherVersion,
	}).Info("Detected cattle version")

	// fetch cattle global properties
	u := fmt.Sprintf("https://raw.githubusercontent.com/rancher/cattle/%s/resources/content/cattle-global.properties", cattleVersion)
	resp, err := c.httpClient.Get(u)
	if err != nil {
		log.WithField("error", err).Warn("Couldn't fetch cattle-global.properties")
		return images
	}
	defer resp.Body.Close()

	s := bufio.NewScanner(resp.Body)
	for s.Scan() {
		t := s.Text()
		x := "lb.instance.image="
		if m, _ := regexp.MatchString(x, t); m {

			images = append(images, strings.TrimPrefix(t, x))
		}
		y := "bootstrap.required.image="
		if m, _ := regexp.MatchString(y, t); m {
			images = append(images, strings.TrimPrefix(t, y))
		}
	}

	return images
}

func (c *Client) detectCattleVersion(rancherVersion string) string {
	// fetch Dockerfile that built rancherVersion
	u := fmt.Sprintf("https://raw.githubusercontent.com/rancher/rancher/%s/server/Dockerfile", rancherVersion)
	resp, err := c.httpClient.Get(u)
	if err != nil {
		log.WithField("error", err).Warn("Couldn't fetch Dockerfile")
		return ""
	}
	defer resp.Body.Close()

	// parse Dockerfile to find cattleVersion
	s := bufio.NewScanner(resp.Body)
	var cattleVersion string
	for s.Scan() {
		if t := strings.Split(s.Text(), " "); len(t) >= 3 && t[0] == "ENV" && t[1] == "CATTLE_CATTLE_VERSION" {
			cattleVersion = t[2]
			break
		}
	}
	return cattleVersion
}
