package fetch

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/Sirupsen/logrus"
)

var (
	DefaultRegistryHost = "index.docker.io"
	DefaultHubNamespace = "docker.io"
	DefaultTag          = "latest"
)

func NewImageRef(name string) *ImageRef {
	return &ImageRef{orig: name}
}

type ImageRef struct {
	orig     string
	name     string
	tag      string
	digest   string
	id       string
	ancestry []string
}

func (ir ImageRef) Host() string {
	// if there are 2 or more slashes and the first element includes a period
	if strings.Count(ir.orig, "/") > 0 {
		// first element
		el := strings.Split(ir.orig, "/")[0]
		// it looks like an address or is localhost
		if strings.Contains(el, ".") || el == "localhost" || strings.Contains(el, ":") {
			return el
		}
	}
	return DefaultHubNamespace
}

func (ir ImageRef) ID() string {
	return ir.id
}
func (ir *ImageRef) SetID(id string) {
	ir.id = id
}

func (ir ImageRef) Ancestry() []string {
	return ir.ancestry
}
func (ir *ImageRef) SetAncestry(ids []string) {
	ir.ancestry = make([]string, len(ids))
	for i := range ids {
		ir.ancestry[i] = ids[i]
	}
}
func (ir ImageRef) Name() string {
	// trim off the hostname plus the slash
	name := strings.TrimPrefix(ir.orig, ir.Host()+"/")

	// check for any tags
	count := strings.Count(name, ":")
	if count == 0 {
		return name
	}
	if count == 1 {
		return strings.Split(name, ":")[0]
	}
	return ""
}
func (ir ImageRef) Tag() string {
	if ir.tag != "" {
		return ir.tag
	}
	count := strings.Count(ir.orig, ":")
	if count == 0 {
		return DefaultTag
	}
	if c := strings.Count(ir.orig, "/"); c > 0 {
		el := strings.Split(ir.orig, "/")[c]
		if strings.Contains(el, ":") {
			return strings.Split(el, ":")[1]
		} else {
			return DefaultTag
		}
	}
	if count == 1 {
		return strings.Split(ir.orig, ":")[1]
	}
	return ""
}

func (ir ImageRef) Digest() string {
	if ir.digest != "" {
		return ir.digest
	}
	return ""
}

func (ir ImageRef) String() string {
	return ir.Host() + "/" + ir.Name() + ":" + ir.Tag()
}

func NewRegistry(host string) RegistryEndpoint {
	if host == "docker.io" {
		host = DefaultRegistryHost
	}
	return RegistryEndpoint{
		Host:      host,
		tokens:    map[string]Token{},
		endpoints: []string{},
	}
}

type RegistryEndpoint struct {
	Host      string
	tokens    map[string]Token
	endpoints []string
}

// Token fetches and returns a fresh Token from this RegistryEndpoint for the imageName provided
func (re *RegistryEndpoint) Token(img *ImageRef) (Token, error) {
	url := fmt.Sprintf("https://%s/v1/repositories/%s/images", re.Host, img.Name())
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return emptyToken, err
	}
	req.Header.Add("X-Docker-Token", "true")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return emptyToken, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return emptyToken, fmt.Errorf("Get(%q) returned %q", url, resp.Status)
	}

	//logrus.Debugf("%#v", resp)

	// looking for header: X-Docker-Token: signature=4709c3e8d96f6a0e9fa53bd205b5be171ac9ade0,repository="vbatts/slackware",access=read
	tok := resp.Header.Get("X-Docker-Token")
	if tok == "" {
		return emptyToken, ErrTokenHeaderEmpty
	}
	endpoint := resp.Header.Get("X-Docker-Endpoints")
	if endpoint != "" {
		re.endpoints = append(re.endpoints, endpoint)
	}

	re.tokens[img.Name()] = Token(tok)
	return re.tokens[img.Name()], nil
}

func (re *RegistryEndpoint) ImageID(img *ImageRef) (string, error) {
	if _, ok := re.tokens[img.Name()]; !ok {
		if _, err := re.Token(img); err != nil {
			return "", err
		}
	}
	endpoint := re.Host
	if len(re.endpoints) > 0 {
		endpoint = re.endpoints[0]
	}
	url := fmt.Sprintf("https://%s/v1/repositories/%s/tags/%s", endpoint, img.Name(), img.Tag())
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Add("Authorization", fmt.Sprintf("Token %s", re.tokens[img.Name()]))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Get(%q) returned %q", url, resp.Status)
	}

	//logrus.Debugf("%#v", resp)
	buf, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	str := strings.Trim(string(buf), "\"")
	img.SetID(str)
	return img.ID(), nil
}

func (re *RegistryEndpoint) Ancestry(img *ImageRef) ([]string, error) {
	emptySet := []string{}
	if _, ok := re.tokens[img.Name()]; !ok {
		if _, err := re.Token(img); err != nil {
			return emptySet, err
		}
	}
	if img.ID() == "" {
		if _, err := re.ImageID(img); err != nil {
			return emptySet, err
		}
	}

	endpoint := re.Host
	if len(re.endpoints) > 0 {
		endpoint = re.endpoints[0]
	}
	url := fmt.Sprintf("https://%s/v1/images/%s/ancestry", endpoint, img.ID())
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return emptySet, err
	}
	req.Header.Add("Authorization", fmt.Sprintf("Token %s", re.tokens[img.Name()]))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return emptySet, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return emptySet, fmt.Errorf("Get(%q) returned %q", url, resp.Status)
	}

	//logrus.Debugf("%#v", resp)
	buf, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return emptySet, err
	}

	set := []string{}
	if err := json.Unmarshal(buf, &set); err != nil {
		return emptySet, err
	}
	img.SetAncestry(set)
	return img.Ancestry(), nil
}

// Return the `repositories` file format data for the referenced image
func FormatRepositories(refs ...*ImageRef) ([]byte, error) {
	// new Registry, ref.Host function
	for _, ref := range refs {
		if ref.ID() == "" {
			re := NewRegistry(ref.Host())
			if _, err := re.ImageID(ref); err != nil {
				return nil, err
			}
		}
	}
	// {"busybox":{"latest":"4986bf8c15363d1c5d15512d5266f8777bfba4974ac56e3270e7760f6f0a8125"}}
	repoInfo := map[string]map[string]string{}
	for _, ref := range refs {
		if repoInfo[ref.Name()] == nil {
			repoInfo[ref.Name()] = map[string]string{ref.Tag(): ref.ID()}
		} else {
			repoInfo[ref.Name()][ref.Tag()] = ref.ID()
		}
	}
	return json.Marshal(repoInfo)
}

// This is presently fetching docker-registry v1 API and returns the IDs of the layers fetched from the registry
func (re *RegistryEndpoint) FetchLayers(img *ImageRef, dest string) ([]string, error) {
	emptySet := []string{}
	if _, ok := re.tokens[img.Name()]; !ok {
		if _, err := re.Token(img); err != nil {
			return emptySet, err
		}
	}
	if img.ID() == "" {
		if _, err := re.ImageID(img); err != nil {
			return emptySet, err
		}
	}
	if len(img.Ancestry()) == 0 {
		if _, err := re.Ancestry(img); err != nil {
			return emptySet, err
		}
	}

	endpoint := re.Host
	if len(re.endpoints) > 0 {
		endpoint = re.endpoints[0]
	}
	for _, id := range img.Ancestry() {
		logrus.Debugf("Fetching layer %s", id)
		if err := os.MkdirAll(path.Join(dest, id), 0755); err != nil {
			return emptySet, err
		}
		// get the json file first
		err := func() error {
			url := fmt.Sprintf("https://%s/v1/images/%s/json", endpoint, id)
			req, err := http.NewRequest("GET", url, nil)
			if err != nil {
				return err
			}
			req.Header.Add("Authorization", fmt.Sprintf("Token %s", re.tokens[img.Name()]))

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("Get(%q) returned %q", url, resp.Status)
			}

			//logrus.Debugf("%#v", resp)
			fh, err := os.Create(path.Join(dest, id, "json"))
			if err != nil {
				return err
			}
			defer fh.Close()
			if _, err := io.Copy(fh, resp.Body); err != nil {
				return err
			}
			return nil
		}()
		if err != nil {
			return emptySet, err
		}

		// get the layer file next
		err = func() error {
			url := fmt.Sprintf("https://%s/v1/images/%s/layer", endpoint, id)
			req, err := http.NewRequest("GET", url, nil)
			if err != nil {
				return err
			}
			logrus.Debugf("%q", fmt.Sprintf("Token %s", re.tokens[img.Name()]))
			req.Header.Add("Authorization", fmt.Sprintf("Token %s", re.tokens[img.Name()]))

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("Get(%q) returned %q", url, resp.Status)
			}

			logrus.Debugf("[FetchLayers] ended up at %q", resp.Request.URL.String())
			logrus.Debugf("[FetchLayers] response %#v", resp)
			fh, err := os.Create(path.Join(dest, id, "layer.tar"))
			if err != nil {
				return err
			}
			defer fh.Close()
			if _, err := io.Copy(fh, resp.Body); err != nil {
				return err
			}
			return nil
		}()
		if err != nil {
			return emptySet, err
		}
	}

	return img.Ancestry(), nil
}

var (
	// ErrTokenHeaderEmpty if the response from the registry did not provide a Token
	ErrTokenHeaderEmpty = fmt.Errorf("HTTP Header x-docker-token is empty")

	emptyToken = Token("")
)

// Token is access token from a docker registry
type Token string

func (t Token) Signature() string {
	return t.getFieldValue("Signature")
}

func (t Token) Repository() string {
	return t.getFieldValue("Repository")
}

func (t Token) Access() string {
	return t.getFieldValue("Access")
}

func (t Token) getFieldValue(key string) string {
	for _, part := range strings.Split(t.String(), ",") {
		if strings.HasPrefix(strings.ToLower(part), strings.ToLower(key)) {
			chunks := strings.SplitN(part, "=", 2)
			if len(chunks) > 2 {
				continue
			}
			return chunks[1]
		}
	}
	return ""
}

// String to satisfy the fmt.Stringer interface
func (t Token) String() string {
	return string(t)
}
