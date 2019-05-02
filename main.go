package main

import (
	"archive/zip"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/CyCoreSystems/go-kamailio/binrpc"
	"github.com/CyCoreSystems/kubetemplate"
	"github.com/CyCoreSystems/netdiscover/discover"
	"github.com/pkg/errors"
)

var maxShortDeaths = 10
var minRuntime = time.Minute
var defaultKamailioRPCPort = "9998"
var coreRoot = "/core"

// Service maintains a Kamailio configuration set in Kubernetes
type Service struct {

	// KamailioRPCPort is the UDP port on which kamailio's RPC service is running.  The default it 9998.
	KamailioRPCPort string

	// Discoverer is the engine which should be used for network discovery
	Discoverer discover.Discoverer

	// CoreRoot is the directory which containe the tree of core configuration templates
	CoreRoot string

	// CustomRoot is the directory which contains the tree of custom configuration templates
	CustomRoot string

	// ProfileRoot is the directory which contains the profile's configuration templates, if any
	ProfileRoot string

	// ExportRoot is the destination directory to which the rendered configuration set will be exported.
	ExportRoot string

	// engine is the template rendering and monitoring engine
	engine *kubetemplate.Engine
}

// nolint: gocyclo
func main() {

	cloud := ""
	if os.Getenv("CLOUD") != "" {
		cloud = os.Getenv("CLOUD")
	}
	disc := getDiscoverer(cloud)

	// Extract profile
	profile := "/source/profile.zip"
	if os.Getenv("PROFILE") != "" {
		profile = os.Getenv("PROFILE")
	}
	profileRoot := "/profile"
	if os.Getenv("PROFILE_DIR") != "" {
		profileRoot = os.Getenv("PROFILE_DIR")
	}
	if err := os.MkdirAll(profileRoot, os.ModePerm); err != nil {
		log.Println("failed to ensure custom directory", profileRoot, ":", err.Error())
		os.Exit(1)
	}
	if err := extractSource(profile, profileRoot); err != nil {
		log.Printf("failed to extract profile (%s): %v", profile, err)
		os.Exit(1)
	}

	// Extract custom
	custom := "/source/custom.zip"
	if os.Getenv("CUSTOM") != "" {
		custom = os.Getenv("CUSTOM")
	}
	customRoot := "/custom"
	if os.Getenv("CUSTOM_DIR") != "" {
		customRoot = os.Getenv("CUSTOM_DIR")
	}
	if err := os.MkdirAll(customRoot, os.ModePerm); err != nil {
		log.Println("failed to ensure custom directory", customRoot, ":", err.Error())
		os.Exit(1)
	}
	if err := extractSource(custom, customRoot); err != nil {
		log.Printf("failed to load source from %s: %s\n", custom, err.Error())
		os.Exit(1)
	}

	exportRoot := "/config/kamailio"
	if os.Getenv("EXPORT_DIR") != "" {
		exportRoot = os.Getenv("EXPORT_DIR")
	}
	if err := os.MkdirAll(exportRoot, os.ModePerm); err != nil {
		log.Println("failed to ensure destination directory", exportRoot, ":", err.Error())
		os.Exit(1)
	}

	var shortDeaths int
	var t time.Time
	for shortDeaths < maxShortDeaths {

		svc := &Service{
			CoreRoot:        coreRoot,
			CustomRoot:      customRoot,
			Discoverer:      disc,
			ExportRoot:      exportRoot,
			KamailioRPCPort: defaultKamailioRPCPort,
			ProfileRoot:     profileRoot,
		}

		t = time.Now()
		log.Println("running service")
		err := svc.Run()
		log.Println("service exited:", err)
		if time.Since(t) < minRuntime {
			shortDeaths++
		} else {
			shortDeaths = 0
		}
	}

	log.Println("kamconfig exiting")
	os.Exit(1)

}

// Run executes the Service
func (s *Service) Run() error {

	renderChan := make(chan error, 1)

	s.engine = kubetemplate.NewEngine(renderChan, s.Discoverer)
	defer s.engine.Close()

	if err := s.render(); err != nil {
		return errors.Wrap(err, "failed initial render")
	}
	s.engine.FirstRenderComplete(true)

	for {
		if err := <-renderChan; err != nil {
			return errors.Wrap(err, "failure during watch")
		}
		log.Println("change detected")

		if err := s.render(); err != nil {
			return errors.Wrap(err, "failed to re-render configuration")
		}

		if err := s.reload(); err != nil {
			return errors.Wrap(err, "failed to kill kamailio")
		}
	}
}

func (s *Service) render() error {
	// Export core
	if err := s.renderCore(); err != nil {
		return errors.Wrap(err, "failed to render core")
	}

	// Export profile
	if err := s.renderProfile(); err != nil {
		return errors.Wrap(err, "failed to render profile")
	}

	// Export custom
	if err := s.renderCustom(); err != nil {
		return errors.Wrap(err, "failed to render initial configuration")
	}

	return nil
}

func (s *Service) renderCore() error {
	return renderDirectory(s.engine, s.CoreRoot, s.ExportRoot)
}

func (s *Service) renderProfile() error {
	return renderDirectory(s.engine, s.ProfileRoot, s.ExportRoot)
}

func (s *Service) renderCustom() error {
	return renderDirectory(s.engine, s.CustomRoot, s.ExportRoot)
}

func getDiscoverer(cloud string) discover.Discoverer {
	switch cloud {
	case "aws":
		return discover.NewAWSDiscoverer()
	case "azure":
		return discover.NewAzureDiscoverer()
	case "digitalocean":
		return discover.NewDigitalOceanDiscoverer()
	case "do":
		return discover.NewDigitalOceanDiscoverer()
	case "gcp":
		return discover.NewGCPDiscoverer()
	case "":
		return discover.NewDiscoverer()
	default:
		log.Printf("WARNING: unhandled cloud %s\n", cloud)
		return discover.NewDiscoverer()
	}
}

func renderDirectory(e *kubetemplate.Engine, sourceRoot, exportRoot string) error {
	var fileCount int

	err := filepath.Walk(sourceRoot, func(fn string, info os.FileInfo, err error) error {
		if err != nil {
			return errors.Wrapf(err, "failed to access file %s", fn)
		}

		outFile := path.Join(exportRoot, strings.TrimPrefix(fn, sourceRoot))
		if info.IsDir() {
			return os.MkdirAll(outFile, os.ModePerm)
		}

		isTemplate := path.Ext(fn) == ".tmpl"
		if isTemplate {
			outFile = strings.TrimSuffix(outFile, ".tmpl")
		}

		in, err := os.Open(fn) // nolint: gosec
		if err != nil {
			return errors.Wrapf(err, "failed to open template for reading: %s", fn)
		}
		defer in.Close() // nolint: errcheck

		if err := os.MkdirAll(path.Dir(outFile), os.ModePerm); err != nil {
			return errors.Wrapf(err, "failed to create destination directory %s", path.Dir(outFile))
		}

		out, err := os.Create(outFile)
		if err != nil {
			return errors.Wrapf(err, "failed to open file for writing: %s", outFile)
		}
		defer out.Close() // nolint: errcheck

		if isTemplate {
			err = kubetemplate.Render(e, in, out)
		} else {
			_, err = io.Copy(out, in)
		}
		if err == nil {
			fileCount++
		}

		return err
	})
	if err != nil {
		return err
	}
	if fileCount < 1 {
		return errors.New("no files processed")
	}
	return nil
}

func (s *Service) reload() error {
	return binrpc.InvokeMethod("core.kill", "localhost", s.KamailioRPCPort)
}

func extractSource(source, customRoot string) (err error) {
	if source == "" {
		return nil
	}
	if customRoot == "" {
		return errors.New("no destination directory")
	}

	if strings.HasPrefix(source, "http") {
		source, err = downloadSource(source)
		if err != nil {
			return errors.Wrap(err, "failed to download source")
		}
	}

	r, err := zip.OpenReader(source)
	if err != nil {
		return errors.Wrap(err, "failed to open source archive")
	}
	defer r.Close() // nolint: errcheck

	for _, f := range r.File {

		in, err := f.Open()
		if err != nil {
			return errors.Wrapf(err, "failed to read file %s", f.Name)
		}
		defer in.Close() // nolint: errcheck

		dest := path.Join(customRoot, f.Name)
		if f.FileInfo().IsDir() {
			if err = os.MkdirAll(dest, os.ModePerm); err != nil {
				return errors.Wrapf(err, "failed to create destination directory %s", f.Name)
			}
			continue
		}

		if err = os.MkdirAll(path.Dir(dest), os.ModePerm); err != nil {
			return errors.Wrapf(err, "failed to create destination directory %s", path.Dir(dest))
		}

		out, err := os.Create(dest)
		if err != nil {
			return errors.Wrapf(err, "failed to create file %s", dest)
		}

		_, err = io.Copy(out, in)
		out.Close() // nolint
		if err != nil {
			return errors.Wrapf(err, "error writing file %s", dest)
		}

	}

	return nil
}

func downloadSource(uri string) (string, error) {
	req, err := http.NewRequest("GET", uri, nil)
	if err != nil {
		return "", errors.Wrapf(err, "failed to construct web request to %s", uri)
	}

	if os.Getenv("URL_USERNAME") != "" {
		req.SetBasicAuth(os.Getenv("URL_USERNAME"), os.Getenv("URL_PASSWORD"))
	}
	if os.Getenv("URL_AUTHORIZATION") != "" {
		req.Header.Add("Authorization", os.Getenv("URL_AUTHORIZATION"))
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() // nolint: errcheck

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", errors.Errorf("request failed: %s", resp.Status)
	}
	if resp.ContentLength < 1 {
		return "", errors.New("empty response")
	}

	tf, err := ioutil.TempFile("", "config-download")
	if err != nil {
		return "", errors.Wrap(err, "failed to create temporary file for download")
	}
	defer tf.Close() // nolint: errcheck

	_, err = io.Copy(tf, resp.Body)

	return tf.Name(), err
}
