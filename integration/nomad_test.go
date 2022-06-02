package integration

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/go-check/check"
	"github.com/hashicorp/nomad/api"
	"github.com/traefik/traefik/v2/integration/try"
)

const nomadVersion = "1.3.0"

type NomadSuite struct {
	BaseSuite

	nomadClient *api.Client
	binary      string
	command     *exec.Cmd
	output      *bytes.Buffer
}

func (ns *NomadSuite) SetUpSuite(c *check.C) {
	// FIXME
	// Nomad cannot [usefully] be run in a docker container or as non-root;
	// instead we run Nomad directly and just make use of it instead of
	// docker-compose.
	// if os.Geteuid() != 0 {
	// 	c.Skip("Skipping Nomad suite, because it requires root privileges")
	// 	return
	// }

	// FIXME needed?
	tmpDir := c.MkDir()
	fmt.Println(tmpDir)

	// install Nomad binary in the given directory
	ns.install(c, tmpDir)

	ns.binary = filepath.Join(tmpDir, "nomad")

	// version check, make sure we can run the binary
	version, err := ns.execute(ns.binary, []string{"version"})
	c.Assert(err, check.IsNil)
	c.Assert(strings.Contains(version, nomadVersion), check.Equals, true)
}

// FIXME rexork
func (ns *NomadSuite) TearDownTest(c *check.C) {
	err := ns.stop()
	c.Assert(err, check.IsNil)
}

func (ns *NomadSuite) install(c *check.C, dir string) {
	archiveName := fmt.Sprintf("nomad_%s_%s_%s.zip", nomadVersion, runtime.GOOS, runtime.GOARCH)
	url := fmt.Sprintf("https://releases.hashicorp.com/nomad/%s/%s", nomadVersion, archiveName)

	archive, err := os.Create(path.Join(dir, archiveName))
	c.Assert(err, check.IsNil)

	res, err := http.Get(url)
	c.Assert(err, check.IsNil)

	defer func() { _ = res.Body.Close() }()

	_, err = io.Copy(archive, res.Body)
	c.Assert(err, check.IsNil)

	_, err = ns.execute("unzip", []string{
		"-o",      // overwrite
		"-d", dir, // destination directory
		path.Join(dir, archiveName), // file to unpack
	})
	c.Assert(err, check.IsNil)
}

// FIXME keep it?
func (ns *NomadSuite) execute(binary string, args []string) (string, error) {
	cmd := exec.Command(binary, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (ns *NomadSuite) start(c *check.C, binary, address string, args []string) {
	ns.command = exec.Command(binary, args...)
	ns.output = new(bytes.Buffer)
	ns.command.Stdout = ns.output
	ns.command.Stderr = ns.output

	err := ns.command.Start()
	c.Assert(err, check.IsNil)

	ns.nomadClient, err = api.NewClient(&api.Config{Address: address})
	c.Assert(err, check.IsNil)

	// Wait for nomad to elect itself.
	err = ns.waitForLeader()
	c.Assert(err, check.IsNil)
}

func (ns *NomadSuite) stop() error {
	_ = ns.command.Process.Signal(syscall.SIGTERM)
	_ = ns.command.Wait()
	output := ns.output.String()
	fmt.Println(output)
	return nil
}

func (ns *NomadSuite) run(filename string, groups int) error {
	b, err := ioutil.ReadFile(filepath.Join("./fixtures/nomad", filename))
	if err != nil {
		return err
	}

	jobString := string(b)

	// set the traefik raw_exec command to the local compiled executable
	traefikAbs, err := filepath.Abs(traefikBinary)
	if err != nil {
		return err
	}

	jobString = strings.Replace(jobString, "EXECUTABLE", traefikAbs, 1)
	job, err := ns.nomadClient.Jobs().ParseHCL(jobString, true)
	if err != nil {
		return err
	}

	// register the job in nomad
	response, _, regErr := ns.nomadClient.Jobs().Register(job, nil)
	if regErr != nil {
		return regErr
	}

	// wait for evaluation to reach complete
	err = try.Do(15*time.Second, func() error {
		info, _, infErr := ns.nomadClient.Evaluations().Info(response.EvalID, nil)
		if infErr != nil {
			return infErr
		}
		if info.Status != "complete" {
			return fmt.Errorf("evaluation not yet complete")
		}
		return nil
	})
	if err != nil {
		return err
	}

	// get allocations for evaluation
	allocs, _, allocsErr := ns.nomadClient.Evaluations().Allocations(response.EvalID, nil)
	if allocsErr != nil {
		return allocsErr
	}

	// check we got the expected number of allocations
	if len(allocs) != groups {
		return fmt.Errorf("expected %d allocations, got %d", groups, len(allocs))
	}

	// wait for tasks in each allocation to be running
	// may involve downloading images from docker; could be a while
	return try.Do(1*time.Minute, func() error {
		for _, allocStub := range allocs {
			alloc, _, allocErr := ns.nomadClient.Allocations().Info(allocStub.ID, nil)
			if allocErr != nil {
				return fmt.Errorf("failed to get alloc %q", allocStub.ID)
			}
			for task, state := range alloc.TaskStates {
				if state.State != "running" {
					return fmt.Errorf("task %q is not yet running, state: %q", task, state.State)
				}
			}
		}
		return nil
	})
}

func (ns *NomadSuite) waitForLeader() error {
	return try.Do(15*time.Second, func() error {
		leader, err := ns.nomadClient.Status().Leader()
		if err != nil || len(leader) == 0 {
			return fmt.Errorf("leader not found. %w", err)
		}
		return nil
	})
}

func (ns *NomadSuite) Test_Defaults(c *check.C) {
	// Start nomad in dev mode (server + client).
	// traefik will be configured with defaults (except refresh interval).
	address := "http://127.0.0.1:4646"

	ns.start(c, ns.binary, address, []string{"agent", "-dev", "-log-level=INFO"})

	// run the basic example
	err := ns.run("exposed_by_default.nomad", 3)
	c.Assert(err, check.IsNil)

	time.Sleep(time.Hour)

	// make request to traefik for whoami service
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:8899/", nil)
	c.Assert(err, check.IsNil)
	req.Host = "whoami"

	// ensure we got an expected response
	err = try.Request(req, 9*time.Second,
		try.StatusCodeIs(200),
		try.BodyContains("Name: whoami-default"))
	c.Assert(err, check.IsNil)

	// make request to traefik for whoami2 service, which is disabled
	req, err = http.NewRequest(http.MethodGet, "http://127.0.0.1:8899/", nil)
	c.Assert(err, check.IsNil)
	req.Host = "whoami2"

	// ensure we got an expected response (404)
	err = try.Request(req, 4*time.Second,
		try.StatusCodeIs(404))
	c.Assert(err, check.IsNil)
}

func (ns *NomadSuite) Test_NotEnabledByDefault(c *check.C) {
	// start nomad in dev mode (server + client)
	// traefik will be configured with .exposedByDefault=false
	address := "http://127.0.0.2:4646"

	ns.start(c, ns.binary, address, []string{"agent", "-dev", "-log-level=INFO", "-bind=127.0.0.2"})

	// run the not-exposed-by-default example
	err := ns.run("not_exposed_by_default.nomad", 3)
	c.Check(err, check.IsNil)

	// make request to traefik for whoami service
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:8899/", nil)
	c.Assert(err, check.IsNil)
	req.Host = "whoami"

	// ensure we got an expected response
	err = try.Request(req, 4*time.Second,
		try.StatusCodeIs(200),
		try.BodyContains("Name: whoami-enabled"))
	c.Assert(err, check.IsNil)

	err = ns.stop()
	c.Check(err, check.IsNil)
}

func (ns *NomadSuite) Test_ConstraintByTag(c *check.C) {
	// start nomad in dev mode (server + client)
	address := "http://127.0.0.3:4646"

	ns.start(c, ns.binary, address, []string{"agent", "-dev", "-log-level=INFO", "-bind=127.0.0.3"})

	// run the example where services are grouped by tags
	err := ns.run("constraint_by_tag.nomad", 3)
	c.Check(err, check.IsNil)

	// make request to traefik for whoami service
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:8899/", nil)
	c.Assert(err, check.IsNil)
	req.Host = "whoami"

	// ensure we got an expected response
	err = try.Request(req, 4*time.Second,
		try.StatusCodeIs(200),
		try.BodyContains("Name: whoami-red"))
	c.Assert(err, check.IsNil)

	err = ns.stop()
	c.Check(err, check.IsNil)
}

func (ns *NomadSuite) Test_QueryByNamespace(c *check.C) {
	// start nomad in dev mode (server + client)
	address := "http://127.0.0.4:4646"

	ns.start(c, ns.binary, address, []string{"agent", "-dev", "-log-level=INFO", "-bind=127.0.0.4"})

	// create the "east" namespace
	_, err := ns.execute(ns.binary, []string{"namespace", "apply", "-address=http://127.0.0.4:4646", "-description=East Side", "east"})
	c.Check(err, check.IsNil)

	// run the example where services are in namespaces
	err = ns.run("query_by_namespace.nomad", 2)
	c.Check(err, check.IsNil)

	// make request to traefik for whoami service
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:8899/", nil)
	c.Assert(err, check.IsNil)
	req.Host = "whoami"

	// ensure we got an expected response
	err = try.Request(req, 4*time.Second,
		try.StatusCodeIs(200),
		try.BodyContains("Name: whoami-east"))
	c.Assert(err, check.IsNil)

	err = ns.stop()
	c.Check(err, check.IsNil)
}
