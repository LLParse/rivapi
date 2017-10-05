package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/blang/semver"
	"github.com/go-yaml/yaml"
	"github.com/gorilla/mux"
)

type Client struct {
	sync.Mutex
	httpClient *http.Client
	token      *Token

	tagList *TagList
	tagHash map[string]string
	hashTag map[string][]string
}

var listen = flag.String("http", ":7070", "address to listen on for http requests")
var rc = flag.Bool("rc", false, "Include Rancher release candidates (RCs) in version list")

func main() {
	flag.Parse()

	c := &Client{
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}

	c.loadTagsPeriodically()

	r := mux.NewRouter()
	r.HandleFunc("/images/{tag}", c.ImageTagHandler)
	r.HandleFunc("/tags", c.TagListHandler)
	r.HandleFunc("/tags/{tag}", c.TagHandler)
	http.Handle("/", r)

	log.WithField("listen", *listen).Info("Starting HTTP server")
	log.Errorf("HTTP Server failure: %s", http.ListenAndServe(*listen, nil))
}

func makeSemver(version string) (semver.Version, error) {
	if version == "" {
		return semver.Version{}, errors.New("empty version")
	}
	return semver.Make(strings.TrimPrefix(version, "v"))
}

func semverContains(v semver.Version, r string) bool {
	k, err := semver.ParseRange(r)
	if err != nil {
		return false
	}
	return k(v)
}

func getCatalogBranch(rancherVersion semver.Version) string {
	if semverContains(rancherVersion, ">1.6.0 <2.0.0") {
		return "v1.6-release"
	} else if semverContains(rancherVersion, ">=2.0.0") {
		return "v2.0-release"
	} else {
		return "master"
	}
}

func (c *Client) ImageTagHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	versionTag := c.findTagAnalog(vars["tag"])
	rancherVersion, err := makeSemver(versionTag)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "Invalid version (%s): %s", versionTag, err)
		return
	}

	url := "https://git.rancher.io/rancher-catalog"
	branch := getCatalogBranch(rancherVersion)
	dir := "rancher-catalog"
	if exists, _ := exists(dir); !exists {

		log.WithFields(log.Fields{
			"branch": branch,
			"url":    url,
		}).Info("Cloning catalog")

		if err := exec.Command("git", "clone", url, "--quiet", "--branch", branch, dir).Run(); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "Error cloning catalog: %s", err)
		}
	} else {

		log.WithFields(log.Fields{
			"branch": branch,
			"url":    url,
		}).Info("Updating catalog")

		// fetch any changes
		if err := exec.Command("git", "-C", dir, "fetch", "origin").Run(); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "Error fetching catalog: %s", err)
			return
		}
		// checkout target branch
		if err := exec.Command("git", "-C", dir, "checkout", branch).Run(); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "Error checking out catalog branch %s: %s", branch, err)
			return
		}
	}

	infraDir := dir + "/infra-templates"
	infraServices, err := ioutil.ReadDir(infraDir)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "Error reading infra-templates dir: %s", err)
		return
	}
	is := &ImageSet{
		Images: []string{},
	}
	for _, infraService := range infraServices {
		if !infraService.IsDir() {
			continue
		}
		serviceDir := infraDir + "/" + infraService.Name()
		versionDir, _ := optimalVersionDir(rancherVersion, serviceDir)
		if versionDir != "" {
			is.Images = append(is.Images, versionImages(serviceDir+"/"+versionDir)...)
		}
	}
	is.Images = append(is.Images, c.getWonkyImages(versionTag)...)

	is.Images = normalize(is.Images)
	data, err := json.Marshal(is)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "Error marshalling response: %s", err)
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, string(data))
}

type VersionDetector struct {
	Version string
}

func detectComposeVersion(data []byte) string {
	version := "1"

	vd := VersionDetector{}
	if err := yaml.Unmarshal(data, &vd); err == nil {
		switch vd.Version {
		case "2":
			version = vd.Version
		}
	}

	return version
}

type DockerComposeV1 struct {
	Services map[string]map[string]interface{} `yaml:"services,inline"`
}

type DockerComposeV2 struct {
	Services map[string]map[string]interface{} `yaml:"services"`
}

func normalize(x []string) []string {
	y := make(map[string]bool)
	z := []string{}
	for _, a := range x {
		y[a] = true
	}
	for a, _ := range y {
		z = append(z, a)
	}
	sort.Strings(z)
	return z
}

func versionImages(versionDir string) []string {
	images := make(map[string]bool)
	images2 := []string{}

	composeFilepath := versionDir + "/docker-compose.yml"
	composeTplFilepath := versionDir + "/docker-compose.yml.tpl"
	var data []byte
	if _, err := os.Stat(composeTplFilepath); err == nil {
		data2, err2 := ioutil.ReadFile(composeTplFilepath)
		if err2 != nil {
			log.Error(err2)
			return images2
		}
		data, err = ApplyTemplating(data2)
		if err != nil {
			log.Error(err)
			return images2
		}
	} else if _, err := os.Stat(composeFilepath); err == nil {
		data, err = ioutil.ReadFile(composeFilepath)
		if err != nil {
			return images2
		}
	}

	services := make(map[string]map[string]interface{})
	switch detectComposeVersion(data) {
	case "1":
		dc := DockerComposeV1{}
		if err := yaml.Unmarshal(data, &dc); err == nil {
			services = dc.Services
		}
	case "2":
		dc := DockerComposeV2{}
		if err := yaml.Unmarshal(data, &dc); err == nil {
			services = dc.Services
		}
	}

	for _, content := range services {
		if image, ok := content["image"]; ok {
			if image != nil {
				images[image.(string)] = true
			} else {
				log.Warnf("Nil image content: %+v", content)
			}
		}
	}

	for image, _ := range images {
		images2 = append(images2, image)
	}
	sort.Strings(images2)
	return images2
}

type RancherCompose struct {
	Catalog *RancherCatalog `yaml:".catalog"`
}

type RancherCatalog struct {
	Version           string `yaml:"version"`
	MinRancherVersion string `yaml:"minimum_rancher_version"`
	MaxRancherVersion string `yaml:"maximum_rancher_version"`
}

type TemplateConfig struct {
	Version string `yaml:"version"`
}

type ImageSet struct {
	Images []string `json:"images"`
}

func optimalVersionDir(rancherVersion semver.Version, serviceDir string) (string, string) {
	serviceVersions, err := ioutil.ReadDir(serviceDir)
	if err != nil {
		log.Fatal(err)
	}

	// Parse each version dir's rancher-compose.yml
	versionDirs := make(map[string]*RancherCompose)
	for _, serviceVersion := range serviceVersions {
		if !serviceVersion.IsDir() {
			continue
		}
		rancherComposePath := serviceDir + "/" + serviceVersion.Name() + "/rancher-compose.yml"
		if _, err := os.Stat(rancherComposePath); err != nil {
			log.Fatal(err)
		}

		data, err := ioutil.ReadFile(rancherComposePath)
		if err != nil {
			log.Fatal(err)
		}
		var rc RancherCompose
		err = yaml.Unmarshal(data, &rc)
		if err != nil {
			log.Fatal(err)
		}
		versionDirs[serviceVersion.Name()] = &rc
	}

	// Filter version dirs by min/max rancher version
	filtered := make(map[string]*RancherCompose)
	for versionDir, rc := range versionDirs {
		if minVersion, err := makeSemver(rc.Catalog.MinRancherVersion); err == nil &&
			rancherVersion.Compare(minVersion) == -1 {
			continue
		}
		if maxVersion, err := makeSemver(rc.Catalog.MaxRancherVersion); err == nil &&
			rancherVersion.Compare(maxVersion) == 1 {
			continue
		}
		filtered[versionDir] = rc
	}

	// Bail out if only one remains
	if len(filtered) == 1 {
		for versionDir, rc := range filtered {
			return versionDir, rc.Catalog.Version
		}
	}

	// Try to return the template version in config.yml
	configPath := serviceDir + "/config.yml"
	if _, err := os.Stat(configPath); err != nil {
		log.Fatal(err)
	}
	data, err := ioutil.ReadFile(configPath)
	if err != nil {
		log.Fatal(err)
	}
	var tc TemplateConfig
	err = yaml.Unmarshal(data, &tc)
	if err != nil {
		log.Fatal(err)
	}
	if tc.Version != "" {
		for versionDir, rc := range filtered {
			if rc.Catalog.Version == tc.Version {
				return versionDir, rc.Catalog.Version
			}
		}
	}

	// Choose the highest ordinal value
	maxVersion := -1
	for versionDir, _ := range filtered {
		if version, err := strconv.Atoi(versionDir); err == nil {
			if version > maxVersion {
				maxVersion = version
			}
		}
	}
	if maxVersion > -1 {
		version := strconv.Itoa(maxVersion)
		return version, filtered[version].Catalog.Version
	}
	return "", "unavailable"
}

func exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return true, err
}

func (c *Client) TagListHandler(w http.ResponseWriter, r *http.Request) {
	response := ""
	for tag, hash := range c.tagHash {
		response = response + fmt.Sprintf("%s: %s\n", tag, hash)
	}
	if data, err := json.Marshal(c.tagList); err == nil {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, string(data))
	} else {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "Error marshalling tag list: %s", err)
	}
}

func (c *Client) TagHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	fmt.Fprintf(w, c.findTagAnalog(vars["tag"]))
}

func (c *Client) findTagAnalog(tag string) string {
	c.Lock()
	defer c.Unlock()
	if hash, ok := c.tagHash[tag]; ok {
		for _, t := range c.hashTag[hash] {
			if t != tag {
				return t
			}
		}
	}
	return tag
}
