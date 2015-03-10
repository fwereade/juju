// Copyright 2015 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package systemd_test

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/coreos/go-systemd/unit"
	"github.com/juju/errors"
	"github.com/juju/names"
	"github.com/juju/testing"
	jc "github.com/juju/testing/checkers"
	"github.com/juju/utils/exec"
	gc "gopkg.in/check.v1"

	"github.com/juju/juju/service"
	"github.com/juju/juju/service/common"
	"github.com/juju/juju/service/systemd"
	coretesting "github.com/juju/juju/testing"
)

const confStr = `
[Unit]
Description=juju agent for %s
After=syslog.target
After=network.target
After=systemd-user-sessions.service

[Service]
Type=forking
ExecStart=%s
RemainAfterExit=yes
Restart=always

[Install]
WantedBy=multi-user.target

`

type confStruct struct {
	Unit struct {
		Description string
		After       []string
	}
	Service struct {
		Type            string
		ExecStart       string
		RemainAfterExit bool
		Restart         string
	}
	Install struct {
		WantedBy string
	}
}

const jujud = "/var/lib/juju/bin/jujud"

var listCmdArg = exec.RunParams{
	Commands: `/bin/systemctl list-unit-files --no-legend --no-page -t service | grep -o -P '^\w[\S]*(?=\.service)'`,
}

type initSystemSuite struct {
	coretesting.BaseSuite

	dataDir string
	ch      chan string
	stub    *testing.Stub
	conn    *systemd.StubDbusAPI
	fops    *systemd.StubFileOps
	exec    *systemd.StubExec

	name    string
	tag     names.Tag
	conf    common.Conf
	confStr string
	service *systemd.Service
}

var _ = gc.Suite(&initSystemSuite{})

func (s *initSystemSuite) SetUpTest(c *gc.C) {
	s.BaseSuite.SetUpTest(c)

	// Patch things out.
	s.dataDir = c.MkDir()
	systemd.PatchFindDataDir(s, s.dataDir)
	s.ch = systemd.PatchNewChan(s)

	s.stub = &testing.Stub{}
	s.conn = systemd.PatchNewConn(s, s.stub)
	s.fops = systemd.PatchFileOps(s, s.stub)
	s.exec = systemd.PatchExec(s, s.stub)

	// Set up the service.
	tagStr := "machine-0"
	tag, err := names.ParseTag(tagStr)
	c.Assert(err, jc.ErrorIsNil)
	s.tag = tag
	s.name = "jujud-" + tagStr
	s.conf = common.Conf{
		Desc:      "juju agent for " + tagStr,
		ExecStart: jujud + " " + tagStr,
	}
	s.service, err = systemd.NewService(s.name, s.conf)
	c.Assert(err, jc.ErrorIsNil)

	// Reset any incidental calls.
	s.stub.Calls = nil
}

func (s *initSystemSuite) newConfStr(name, cmd string) string {
	tag := name[len("jujud-"):]
	if cmd == "" {
		cmd = jujud + " " + tag
	}
	return fmt.Sprintf(confStr[1:], tag, cmd)
}

func (s *initSystemSuite) addService(name, status string) {
	tag := name[len("jujud-"):]
	desc := "juju agent for " + tag
	s.conn.AddService(name, desc, status)
}

func (s *initSystemSuite) addListResponse() {
	var lines []string
	for _, unit := range s.conn.Units {
		lines = append(lines, strings.TrimSuffix(unit.Name, ".service"))
	}

	s.exec.Responses = append(s.exec.Responses, exec.ExecResponse{
		Code:   0,
		Stdout: []byte(strings.Join(lines, "\n")),
		Stderr: nil,
	})
}

func (s *initSystemSuite) setConf(c *gc.C, conf common.Conf) {
	data, err := systemd.Serialize(s.name, conf)
	c.Assert(err, jc.ErrorIsNil)
	s.exec.Responses = append(s.exec.Responses, exec.ExecResponse{
		Code:   0,
		Stdout: data,
		Stderr: nil,
	})
}

func (s *initSystemSuite) checkCreateFileCall(c *gc.C, index int, filename, content string, perm os.FileMode) {
	if content == "" {
		name := filename
		filename = fmt.Sprintf("%s/init/%s/%s.service", s.dataDir, name, name)
		content = s.newConfStr(name, "")
	}

	call := s.stub.Calls[index]
	if !c.Check(call.FuncName, gc.Equals, "CreateFile") {
		return
	}
	if !c.Check(call.Args, gc.HasLen, 3) {
		return
	}

	callFilename, callData, callPerm := call.Args[0], call.Args[1], call.Args[2]
	c.Check(callFilename, gc.Equals, filename)

	// Some tests don't generate valid ini files, instead including placeholder
	// strings (e.g. "a\nb\nc\n"). To avoid parsing errors, we only try and
	// parse actual and expected file content if they don't exactly match.
	if content != string(callData.([]byte)) {
		// Parse the ini configurations and compare those.
		expected, err := unit.Deserialize(bytes.NewReader(callData.([]byte)))
		c.Assert(err, jc.ErrorIsNil)
		cfg, err := unit.Deserialize(strings.NewReader(content))
		c.Assert(err, jc.ErrorIsNil)
		c.Check(cfg, jc.SameContents, expected)
	}

	c.Check(callPerm, gc.Equals, perm)
}

func (s *initSystemSuite) TestListServices(c *gc.C) {
	s.addService("jujud-machine-0", "active")
	s.addService("something-else", "error")
	s.addService("jujud-unit-wordpress-0", "active")
	s.addService("another", "inactive")
	s.addListResponse()

	names, err := systemd.ListServices()
	c.Assert(err, jc.ErrorIsNil)

	c.Check(names, jc.SameContents, []string{
		"jujud-machine-0",
		"something-else",
		"jujud-unit-wordpress-0",
		"another",
	})
	s.stub.CheckCallNames(c, "RunCommand")
}

func (s *initSystemSuite) TestListServicesEmpty(c *gc.C) {
	s.addListResponse()

	names, err := systemd.ListServices()
	c.Assert(err, jc.ErrorIsNil)

	c.Check(names, gc.HasLen, 0)
	s.stub.CheckCallNames(c, "RunCommand")
}

func (s *initSystemSuite) TestNewService(c *gc.C) {
	service, err := systemd.NewService(s.name, s.conf)
	c.Assert(err, jc.ErrorIsNil)

	c.Check(service, jc.DeepEquals, &systemd.Service{
		Service: common.Service{
			Name: s.name,
			Conf: s.conf,
		},
		ConfName: s.name + ".service",
		UnitName: s.name + ".service",
		Dirname:  fmt.Sprintf("%s/init/%s", s.dataDir, s.name),
	})
	s.stub.CheckCalls(c, nil)
}

func (s *initSystemSuite) TestNewServiceLogfile(c *gc.C) {
	s.conf.Logfile = "/var/log/juju/machine-0.log"

	service, err := systemd.NewService(s.name, s.conf)
	c.Assert(err, jc.ErrorIsNil)

	c.Check(service.Service.Conf, jc.DeepEquals, common.Conf{
		Desc:      s.conf.Desc,
		ExecStart: s.conf.ExecStart + " &> /var/log/juju/machine-0.log",
	})
}

func (s *initSystemSuite) TestNewServiceEmptyConf(c *gc.C) {
	service, err := systemd.NewService(s.name, common.Conf{})
	c.Assert(err, jc.ErrorIsNil)

	c.Check(service, jc.DeepEquals, &systemd.Service{
		Service: common.Service{
			Name: s.name,
		},
		ConfName: s.name + ".service",
		UnitName: s.name + ".service",
		Dirname:  fmt.Sprintf("%s/init/%s", s.dataDir, s.name),
	})
	s.stub.CheckCalls(c, nil)
}

func (s *initSystemSuite) TestUpdateConfig(c *gc.C) {
	s.conf.ExecStart = "/path/to/some/other/command"
	c.Assert(s.service.Service.Conf.ExecStart, gc.Equals, jujud+" machine-0")

	s.service.UpdateConfig(s.conf)

	c.Check(s.service, jc.DeepEquals, &systemd.Service{
		Service: common.Service{
			Name: s.name,
			Conf: s.conf,
		},
		ConfName: s.name + ".service",
		UnitName: s.name + ".service",
		Dirname:  fmt.Sprintf("%s/init/%s", s.dataDir, s.name),
	})
	s.stub.CheckCalls(c, nil)
}

func (s *initSystemSuite) TestUpdateConfigExtraScript(c *gc.C) {
	s.conf.ExtraScript = "/path/to/another/command"

	s.service.UpdateConfig(s.conf)

	dirname := fmt.Sprintf("%s/init/%s", s.dataDir, s.name)
	script := "/path/to/another/command\n" + jujud + " machine-0"
	c.Check(s.service, jc.DeepEquals, &systemd.Service{
		Service: common.Service{
			Name: s.name,
			Conf: common.Conf{
				Desc:      s.conf.Desc,
				ExecStart: dirname + "/exec-start.sh",
			},
		},
		UnitName: s.name + ".service",
		ConfName: s.name + ".service",
		Dirname:  dirname,
		Script:   []byte(script),
	})
	c.Check(string(s.service.Script), gc.Equals, script)
	s.stub.CheckCalls(c, nil)
}

func (s *initSystemSuite) TestUpdateConfigMultiline(c *gc.C) {
	s.conf.ExecStart = "a\nb\nc"

	s.service.UpdateConfig(s.conf)

	dirname := fmt.Sprintf("%s/init/%s", s.dataDir, s.name)
	c.Check(s.service, jc.DeepEquals, &systemd.Service{
		Service: common.Service{
			Name: s.name,
			Conf: common.Conf{
				Desc:      s.conf.Desc,
				ExecStart: dirname + "/exec-start.sh",
			},
		},
		UnitName: s.name + ".service",
		ConfName: s.name + ".service",
		Dirname:  dirname,
		Script:   []byte("a\nb\nc"),
	})
	s.stub.CheckCalls(c, nil)
}

func (s *initSystemSuite) TestUpdateConfigLogfile(c *gc.C) {
	s.conf.Logfile = "/var/log/juju/machine-0.log"

	s.service.UpdateConfig(s.conf)

	// TODO(ericsnow) The error return needs to be checked once there is one.
	c.Check(s.service.Service.Conf, jc.DeepEquals, common.Conf{
		Desc:      s.conf.Desc,
		ExecStart: s.conf.ExecStart + " &> /var/log/juju/machine-0.log",
	})
}

func (s *initSystemSuite) TestUpdateConfigEmpty(c *gc.C) {
	s.service.UpdateConfig(common.Conf{})

	c.Check(s.service, jc.DeepEquals, &systemd.Service{
		Service: common.Service{
			Name: s.name,
		},
		ConfName: s.name + ".service",
		UnitName: s.name + ".service",
		Dirname:  fmt.Sprintf("%s/init/%s", s.dataDir, s.name),
	})
	s.stub.CheckCalls(c, nil)
}

func (s *initSystemSuite) TestInstalledTrue(c *gc.C) {
	s.addService("jujud-machine-0", "active")
	s.addService("something-else", "error")
	s.addService("juju-mongod", "active")
	s.addListResponse()

	installed, err := s.service.Installed()
	c.Assert(err, jc.ErrorIsNil)

	c.Check(installed, jc.IsTrue)
	s.stub.CheckCallNames(c, "RunCommand")
}

func (s *initSystemSuite) TestInstalledFalse(c *gc.C) {
	s.addService("something-else", "error")
	s.addListResponse()

	installed, err := s.service.Installed()
	c.Assert(err, jc.ErrorIsNil)

	c.Check(installed, jc.IsFalse)
	s.stub.CheckCallNames(c, "RunCommand")
}

func (s *initSystemSuite) TestInstalledError(c *gc.C) {
	s.addService("jujud-machine-0", "active")
	s.addService("something-else", "error")
	s.addService("juju-mongod", "active")
	s.addListResponse()
	failure := errors.New("<failed>")
	s.stub.SetErrors(failure)

	installed, err := s.service.Installed()
	c.Assert(errors.Cause(err), gc.Equals, failure)

	c.Check(installed, jc.IsFalse)
	s.stub.CheckCallNames(c, "RunCommand")
}

func (s *initSystemSuite) TestExistsTrue(c *gc.C) {
	s.setConf(c, s.conf)

	exists, err := s.service.Exists()
	c.Assert(err, jc.ErrorIsNil)

	c.Check(exists, jc.IsTrue)
	s.stub.CheckCallNames(c, "RunCommand")
}

func (s *initSystemSuite) TestExistsFalse(c *gc.C) {
	// We force the systemd API to return a slightly different conf.
	// In this case we simply set Conf.Env, which s.conf does not set.
	// This causes Service.Exists to return false.
	s.setConf(c, common.Conf{
		Desc:      s.conf.Desc,
		ExecStart: s.conf.ExecStart,
		Env:       map[string]string{"a": "b"},
	})

	exists, err := s.service.Exists()
	c.Assert(err, jc.ErrorIsNil)

	c.Check(exists, jc.IsFalse)
	s.stub.CheckCallNames(c, "RunCommand")
}

func (s *initSystemSuite) TestExistsError(c *gc.C) {
	failure := errors.New("<failed>")
	s.stub.SetErrors(failure)

	exists, err := s.service.Exists()
	c.Assert(errors.Cause(err), gc.Equals, failure)

	c.Check(exists, jc.IsFalse)
	s.stub.CheckCallNames(c, "RunCommand")
}

func (s *initSystemSuite) TestExistsEmptyConf(c *gc.C) {
	s.service.Service.Conf = common.Conf{}

	_, err := s.service.Exists()

	c.Check(err, gc.ErrorMatches, `.*no conf expected.*`)
	s.stub.CheckCalls(c, nil)
}

func (s *initSystemSuite) TestRunningTrue(c *gc.C) {
	s.addService("jujud-machine-0", "active")
	s.addService("something-else", "error")
	s.addService("juju-mongod", "active")

	running, err := s.service.Running()
	c.Assert(err, jc.ErrorIsNil)

	c.Check(running, jc.IsTrue)
	s.stub.CheckCallNames(c, "ListUnits", "Close")
}

func (s *initSystemSuite) TestRunningFalse(c *gc.C) {
	s.addService("jujud-machine-0", "inactive")
	s.addService("something-else", "error")
	s.addService("juju-mongod", "active")

	running, err := s.service.Running()
	c.Assert(err, jc.ErrorIsNil)

	c.Check(running, jc.IsFalse)
	s.stub.CheckCallNames(c, "ListUnits", "Close")
}

func (s *initSystemSuite) TestRunningNotEnabled(c *gc.C) {
	s.addService("something-else", "active")

	running, err := s.service.Running()
	c.Assert(err, jc.ErrorIsNil)

	c.Check(running, jc.IsFalse)
	s.stub.CheckCallNames(c, "ListUnits", "Close")
}

func (s *initSystemSuite) TestRunningError(c *gc.C) {
	s.addService("jujud-machine-0", "active")
	s.addService("something-else", "error")
	s.addService("juju-mongod", "active")
	failure := errors.New("<failed>")
	s.stub.SetErrors(failure)

	running, err := s.service.Running()
	c.Assert(errors.Cause(err), gc.Equals, failure)

	c.Check(running, jc.IsFalse)
	s.stub.CheckCallNames(c, "ListUnits", "Close")
}

func (s *initSystemSuite) TestStart(c *gc.C) {
	s.addService("jujud-machine-0", "inactive")
	s.ch <- "done"
	s.addListResponse()

	err := s.service.Start()
	c.Assert(err, jc.ErrorIsNil)

	s.stub.CheckCalls(c, []testing.StubCall{{
		FuncName: "RunCommand",
		Args: []interface{}{
			listCmdArg,
		},
	}, {
		FuncName: "ListUnits",
	}, {
		FuncName: "Close",
	}, {
		FuncName: "StartUnit",
		Args: []interface{}{
			s.name + ".service",
			"fail",
			(chan<- string)(s.ch),
		},
	}, {
		FuncName: "Close",
	}})
}

func (s *initSystemSuite) TestStartAlreadyRunning(c *gc.C) {
	s.addService("jujud-machine-0", "active")
	s.ch <- "done" // just in case
	s.addListResponse()

	err := s.service.Start()
	c.Assert(err, jc.ErrorIsNil)

	s.stub.CheckCallNames(c,
		"RunCommand",
		"ListUnits",
		"Close",
	)
}

func (s *initSystemSuite) TestStartNotInstalled(c *gc.C) {
	s.ch <- "done" // just in case

	err := s.service.Start()

	c.Check(err, jc.Satisfies, errors.IsNotFound)
	s.stub.CheckCallNames(c, "RunCommand")
}

func (s *initSystemSuite) TestStop(c *gc.C) {
	s.addService("jujud-machine-0", "active")
	s.ch <- "done"

	err := s.service.Stop()
	c.Assert(err, jc.ErrorIsNil)

	s.stub.CheckCalls(c, []testing.StubCall{{
		FuncName: "ListUnits",
	}, {
		FuncName: "Close",
	}, {
		FuncName: "StopUnit",
		Args: []interface{}{
			s.name + ".service",
			"fail",
			(chan<- string)(s.ch),
		},
	}, {
		FuncName: "Close",
	}})
}

func (s *initSystemSuite) TestStopNotRunning(c *gc.C) {
	s.addService("jujud-machine-0", "inactive")
	s.ch <- "done" // just in case

	err := s.service.Stop()
	c.Assert(err, jc.ErrorIsNil)

	s.stub.CheckCallNames(c, "ListUnits", "Close")
}

func (s *initSystemSuite) TestStopNotInstalled(c *gc.C) {
	s.ch <- "done" // just in case

	err := s.service.Stop()
	c.Assert(err, jc.ErrorIsNil)

	s.stub.CheckCallNames(c, "ListUnits", "Close")
}

func (s *initSystemSuite) TestRemove(c *gc.C) {
	s.addService("jujud-machine-0", "inactive")
	s.addListResponse()

	err := s.service.Remove()
	c.Assert(err, jc.ErrorIsNil)

	s.stub.CheckCallNames(c,
		"RunCommand",
		"DisableUnitFiles",
		"RemoveAll",
		"Close",
	)
	s.stub.CheckCalls(c, []testing.StubCall{{
		FuncName: "RunCommand",
		Args: []interface{}{
			listCmdArg,
		},
	}, {
		FuncName: "DisableUnitFiles",
		Args: []interface{}{
			[]string{s.name + ".service"},
			false,
		},
	}, {
		FuncName: "RemoveAll",
		Args: []interface{}{
			fmt.Sprintf("%s/init/%s", s.dataDir, s.name),
		},
	}, {
		FuncName: "Close",
	}})
}

func (s *initSystemSuite) TestRemoveNotInstalled(c *gc.C) {
	err := s.service.Remove()
	c.Assert(err, jc.ErrorIsNil)

	s.stub.CheckCallNames(c, "RunCommand")
}

func (s *initSystemSuite) TestInstall(c *gc.C) {
	err := s.service.Install()
	c.Assert(err, jc.ErrorIsNil)

	dirname := fmt.Sprintf("%s/init/%s", s.dataDir, s.name)
	filename := fmt.Sprintf("%s/%s.service", dirname, s.name)
	createFileOutput := s.stub.Calls[2].Args[1]
	s.stub.CheckCalls(c, []testing.StubCall{{
		FuncName: "RunCommand",
		Args: []interface{}{
			listCmdArg,
		},
	}, {
		FuncName: "MkdirAll",
		Args: []interface{}{
			dirname,
		},
	}, {
		FuncName: "CreateFile",
		Args: []interface{}{
			filename,
			// The contents of the file will always pass this test. We are
			// testing the sequence of commands. The output of CreateFile
			// is tested by tests that call checkCreateFileCall.
			createFileOutput,
			os.FileMode(0644),
		},
	}, {
		FuncName: "LinkUnitFiles",
		Args: []interface{}{
			[]string{filename},
			false,
			true,
		},
	}, {
		FuncName: "EnableUnitFiles",
		Args: []interface{}{
			[]string{filename},
			false,
			true,
		},
	}, {
		FuncName: "Close",
	}})
	s.checkCreateFileCall(c, 2, filename, s.newConfStr(s.name, ""), 0644)
}

func (s *initSystemSuite) TestInstallAlreadyInstalled(c *gc.C) {
	s.addService("jujud-machine-0", "inactive")
	s.addListResponse()
	s.setConf(c, s.conf)

	err := s.service.Install()
	c.Assert(err, jc.ErrorIsNil)

	s.stub.CheckCallNames(c,
		"RunCommand",
		"RunCommand",
	)
}

func (s *initSystemSuite) TestInstallZombie(c *gc.C) {
	s.addService("jujud-machine-0", "active")
	s.addListResponse()
	// We force the systemd API to return a slightly different conf.
	// In this case we simply set Conf.Env, which s.conf does not set.
	// This causes Service.Exists to return false.
	s.setConf(c, common.Conf{
		Desc:      s.conf.Desc,
		ExecStart: s.conf.ExecStart,
		Env:       map[string]string{"a": "b"},
	})
	s.addListResponse()
	s.ch <- "done"

	err := s.service.Install()
	c.Assert(err, jc.ErrorIsNil)

	s.stub.CheckCallNames(c,
		"RunCommand",
		"RunCommand",
		"ListUnits",
		"Close",
		"StopUnit",
		"Close",
		"RunCommand",
		"DisableUnitFiles",
		"RemoveAll",
		"Close",
		"MkdirAll",
		"CreateFile",
		"LinkUnitFiles",
		"EnableUnitFiles",
		"Close",
	)
	s.checkCreateFileCall(c, 11, s.name, "", 0644)
}

func (s *initSystemSuite) TestInstallMultiline(c *gc.C) {
	scriptPath := fmt.Sprintf("%s/init/%s/exec-start.sh", s.dataDir, s.name)
	cmd := "a\nb\nc"
	s.service.Service.Conf.ExecStart = scriptPath
	s.service.Script = []byte(cmd)

	err := s.service.Install()
	c.Assert(err, jc.ErrorIsNil)

	s.stub.CheckCallNames(c,
		"RunCommand",
		"MkdirAll",
		"CreateFile",
		"CreateFile",
		"LinkUnitFiles",
		"EnableUnitFiles",
		"Close",
	)
	s.checkCreateFileCall(c, 2, scriptPath, cmd, 0755)
	filename := fmt.Sprintf("%s/init/%s/%s.service", s.dataDir, s.name, s.name)
	content := s.newConfStr(s.name, scriptPath)
	s.checkCreateFileCall(c, 3, filename, content, 0644)
}

func (s *initSystemSuite) TestInstallEmptyConf(c *gc.C) {
	s.service.Service.Conf = common.Conf{}

	err := s.service.Install()

	c.Check(err, gc.ErrorMatches, `.*missing conf.*`)
	s.stub.CheckCalls(c, nil)
}

func (s *initSystemSuite) TestInstallCommands(c *gc.C) {
	commands, err := s.service.InstallCommands()
	c.Assert(err, jc.ErrorIsNil)

	content := s.newConfStr("jujud-machine-0", "")
	header := "cat >> /tmp/jujud-machine-0.service << 'EOF'\n"
	footer := "EOF"

	// Parse the ini configurations and compare those.
	expectedString := commands[0][len(header) : len(commands[0])-len(footer)]
	expected, err := unit.Deserialize(strings.NewReader(expectedString))
	c.Assert(err, jc.ErrorIsNil)
	cfg, err := unit.Deserialize(strings.NewReader(content))
	c.Assert(err, jc.ErrorIsNil)
	c.Check(cfg, jc.SameContents, expected)

	cmd := commands[0]
	c.Check(cmd, jc.HasPrefix, header)
	c.Check(cmd, jc.HasSuffix, footer)
	c.Check(commands[1:], jc.DeepEquals, []string{
		"/bin/systemctl link /tmp/jujud-machine-0.service",
		"/bin/systemctl enable jujud-machine-0.service",
	})
}

// parseConfSections is a poor man's ini parser.
func parseConfSections(lines []string) map[string][]string {
	sections := make(map[string][]string)

	var section string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			if section != "" {
				sort.Strings(sections[section])
			}
			section = line[1 : len(line)-1]
			sections[section] = nil
		} else {
			sections[section] = append(sections[section], line)
		}
	}
	if section != "" {
		sort.Strings(sections[section])
	}

	return sections
}

func (s *initSystemSuite) TestInstallCommandsShutdown(c *gc.C) {
	// This test  must be done without regard to map order.

	name := "juju-shutdown-job"
	conf, err := service.ShutdownAfterConf("cloud-final")
	c.Assert(err, jc.ErrorIsNil)
	svc, err := systemd.NewService(name, conf)
	c.Assert(err, jc.ErrorIsNil)
	commands, err := svc.InstallCommands()
	c.Assert(err, jc.ErrorIsNil)

	c.Assert(commands, gc.HasLen, 3)

	// Parse the first command.
	cmd := strings.TrimSpace(commands[0])
	lines := strings.Split(cmd, "\n")
	header := lines[0]
	footer := lines[len(lines)-1]
	sections := parseConfSections(lines[1 : len(lines)-1])

	// Check the cat portion.
	c.Check(header, gc.Equals, "cat >> /tmp/juju-shutdown-job.service << 'EOF'")
	c.Check(footer, gc.Equals, "EOF")

	// Check the conf portion.
	content := `
[Unit]
Description=juju shutdown job
After=syslog.target
After=network.target
After=systemd-user-sessions.service
After=cloud-final
Conflicts=cloud-final

[Service]
ExecStart=/sbin/shutdown -h now
ExecStopPost=/bin/systemctl disable juju-shutdown-job.service
`
	expected := parseConfSections(strings.Split(content[1:], "\n"))
	c.Check(sections, jc.DeepEquals, expected)

	// Check the remaining commands.
	c.Check(commands[1:], jc.DeepEquals, []string{
		"/bin/systemctl link /tmp/juju-shutdown-job.service",
		"/bin/systemctl enable juju-shutdown-job.service",
	})
}

func (s *initSystemSuite) TestInstallCommandsEmptyConf(c *gc.C) {
	s.service.Service.Conf = common.Conf{}

	_, err := s.service.InstallCommands()

	c.Check(err, gc.ErrorMatches, `.*missing conf.*`)
	s.stub.CheckCalls(c, nil)
}

func (s *initSystemSuite) TestStartCommands(c *gc.C) {
	commands, err := s.service.StartCommands()
	c.Assert(err, jc.ErrorIsNil)

	c.Check(commands, jc.DeepEquals, []string{
		"/bin/systemctl start jujud-machine-0.service",
	})
}
