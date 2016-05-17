// Copyright 2016 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

// +build go1.3

package lxd

import (
	"errors"
	"fmt"
	"net"

	jc "github.com/juju/testing/checkers"
	"github.com/juju/utils/packaging/commands"
	"github.com/juju/utils/packaging/manager"
	"github.com/juju/utils/series"
	gc "gopkg.in/check.v1"

	"github.com/juju/juju/testing"
)

type InitialiserSuite struct {
	testing.BaseSuite
	calledCmds []string
}

var _ = gc.Suite(&InitialiserSuite{})

const lxdBridgeContent = `# WARNING: Don't modify this file by hand, it is generated by debconf!
# To update those values, please run "dpkg-reconfigure lxd"

# Whether to setup a new bridge
USE_LXD_BRIDGE="true"
EXISTING_BRIDGE=""

# Bridge name
LXD_BRIDGE="lxdbr0"

# dnsmasq configuration path
LXD_CONFILE=""

# dnsmasq domain
LXD_DOMAIN="lxd"

# IPv4
LXD_IPV4_ADDR="10.0.4.1"
LXD_IPV4_NETMASK="255.255.255.0"
LXD_IPV4_NETWORK="10.0.4.1/24"
LXD_IPV4_DHCP_RANGE="10.0.4.2,10.0.4.100"
LXD_IPV4_DHCP_MAX="50"
LXD_IPV4_NAT="true"

# IPv6
LXD_IPV6_ADDR="2001:470:b2b5:9999::1"
LXD_IPV6_MASK="64"
LXD_IPV6_NETWORK="2001:470:b2b5:9999::1/64"
LXD_IPV6_NAT="true"

# Proxy server
LXD_IPV6_PROXY="true"
`

// getMockRunCommandWithRetry is a helper function which returns a function
// with an identical signature to manager.RunCommandWithRetry which saves each
// command it recieves in a slice and always returns no output, error code 0
// and a nil error.
func getMockRunCommandWithRetry(calledCmds *[]string) func(string, func(string) error) (string, int, error) {
	return func(cmd string, fatalError func(string) error) (string, int, error) {
		*calledCmds = append(*calledCmds, cmd)
		return "", 0, nil
	}
}

func (s *InitialiserSuite) SetUpTest(c *gc.C) {
	s.BaseSuite.SetUpTest(c)
	s.calledCmds = []string{}
	s.PatchValue(&manager.RunCommandWithRetry, getMockRunCommandWithRetry(&s.calledCmds))
	s.PatchValue(&configureZFS, func() {})
	s.PatchValue(&configureLXDBridge, func() error { return nil })
}

func (s *InitialiserSuite) TestLTSSeriesPackages(c *gc.C) {
	// Momentarily, the only series with a dedicated cloud archive is precise,
	// which we will use for the following test:
	paccmder, err := commands.NewPackageCommander("trusty")
	c.Assert(err, jc.ErrorIsNil)

	s.PatchValue(&series.HostSeries, func() string { return "trusty" })
	container := NewContainerInitialiser("trusty")

	err = container.Initialise()
	c.Assert(err, jc.ErrorIsNil)

	c.Assert(s.calledCmds, gc.DeepEquals, []string{
		paccmder.InstallCmd("--target-release", "trusty-backports", "lxd"),
	})
}

func (s *InitialiserSuite) TestNoSeriesPackages(c *gc.C) {
	// Here we want to test for any other series whilst avoiding the
	// possibility of hitting a cloud archive-requiring release.
	// As such, we simply pass an empty series.
	paccmder, err := commands.NewPackageCommander("xenial")
	c.Assert(err, jc.ErrorIsNil)

	container := NewContainerInitialiser("")

	err = container.Initialise()
	c.Assert(err, jc.ErrorIsNil)

	c.Assert(s.calledCmds, gc.DeepEquals, []string{
		paccmder.InstallCmd("lxd"),
	})
}

func (s *InitialiserSuite) TestFindAvailableSubnetWithInterfaceAddrsError(c *gc.C) {
	s.PatchValue(&interfaceAddrs, func() ([]net.Addr, error) {
		return nil, errors.New("boom!")
	})
	subnet, err := findNextAvailableIPv4Subnet()
	c.Assert(err, gc.ErrorMatches, "cannot get network interface addresses: boom!")
	c.Assert(subnet, gc.Equals, "")
}

type testFindSubnetAddr struct {
	val string
}

func (a testFindSubnetAddr) Network() string {
	return "ip+net"
}

func (a testFindSubnetAddr) String() string {
	return a.val
}

func testAddresses(c *gc.C, networks ...string) ([]net.Addr, error) {
	addrs := make([]net.Addr, 0)
	for _, n := range networks {
		_, _, err := net.ParseCIDR(n)
		if err != nil {
			return nil, err
		}
		c.Assert(err, gc.IsNil)
		addrs = append(addrs, testFindSubnetAddr{n})
	}
	return addrs, nil
}

func (s *InitialiserSuite) TestFindAvailableSubnetWithNoAddresses(c *gc.C) {
	s.PatchValue(&interfaceAddrs, func() ([]net.Addr, error) {
		return testAddresses(c)
	})
	subnet, err := findNextAvailableIPv4Subnet()
	c.Assert(err, gc.IsNil)
	c.Assert(subnet, gc.Equals, "0")
}

func (s *InitialiserSuite) TestFindAvailableSubnetWithIPv6Only(c *gc.C) {
	s.PatchValue(&interfaceAddrs, func() ([]net.Addr, error) {
		return testAddresses(c, "fe80::aa8e:a275:7ae0:34af/64")
	})
	subnet, err := findNextAvailableIPv4Subnet()
	c.Assert(err, gc.IsNil)
	c.Assert(subnet, gc.Equals, "0")
}

func (s *InitialiserSuite) TestFindAvailableSubnetWithIPv4OnlyAndNo10xSubnet(c *gc.C) {
	s.PatchValue(&interfaceAddrs, func() ([]net.Addr, error) {
		return testAddresses(c, "192.168.1.64/24")
	})
	subnet, err := findNextAvailableIPv4Subnet()
	c.Assert(err, gc.IsNil)
	c.Assert(subnet, gc.Equals, "0")
}

func (s *InitialiserSuite) TestFindAvailableSubnetWithInvalidCIDR(c *gc.C) {
	s.PatchValue(&interfaceAddrs, func() ([]net.Addr, error) {
		return []net.Addr{
			testFindSubnetAddr{"10.0.0.1"},
			testFindSubnetAddr{"10.0.5.1/24"}}, nil
	})
	subnet, err := findNextAvailableIPv4Subnet()
	c.Assert(err, gc.IsNil)
	c.Assert(subnet, gc.Equals, "6")
}

func (s *InitialiserSuite) TestFindAvailableSubnetWithIPv4AndExisting10xNetwork(c *gc.C) {
	s.PatchValue(&interfaceAddrs, func() ([]net.Addr, error) {
		return testAddresses(c, "192.168.1.64/24", "10.0.0.1/24")
	})
	subnet, err := findNextAvailableIPv4Subnet()
	c.Assert(err, gc.IsNil)
	c.Assert(subnet, gc.Equals, "1")
}

func (s *InitialiserSuite) TestFindAvailableSubnetWithExisting10xNetworks(c *gc.C) {
	s.PatchValue(&interfaceAddrs, func() ([]net.Addr, error) {
		return testAddresses(c, "192.168.1.0/24", "10.0.4.1/24", "::1/128", "10.0.3.1/24", "fe80::aa8e:a275:7ae0:34af/64")
	})
	subnet, err := findNextAvailableIPv4Subnet()
	c.Assert(err, gc.IsNil)
	c.Assert(subnet, gc.Equals, "5")
}

func (s *InitialiserSuite) TestFindAvailableSubnetUpperBoundInUse(c *gc.C) {
	s.PatchValue(&interfaceAddrs, func() ([]net.Addr, error) {
		return testAddresses(c, "10.0.255.1/24")
	})
	subnet, err := findNextAvailableIPv4Subnet()
	c.Assert(err, gc.IsNil)
	c.Assert(subnet, gc.Equals, "0")
}

func (s *InitialiserSuite) TestFindAvailableSubnetUpperBoundAndLowerBoundInUse(c *gc.C) {
	s.PatchValue(&interfaceAddrs, func() ([]net.Addr, error) {
		return testAddresses(c, "10.0.255.1/24", "10.0.0.1/24")
	})
	subnet, err := findNextAvailableIPv4Subnet()
	c.Assert(err, gc.IsNil)
	c.Assert(subnet, gc.Equals, "1")
}

func (s *InitialiserSuite) TestFindAvailableSubnetWithFull10xSubnet(c *gc.C) {
	s.PatchValue(&interfaceAddrs, func() ([]net.Addr, error) {
		addrs := make([]net.Addr, 256)
		for i := 0; i < 256; i++ {
			subnet := fmt.Sprintf("10.0.%v.1/24", i)
			addrs[i] = testFindSubnetAddr{subnet}
		}
		return addrs, nil
	})
	subnet, err := findNextAvailableIPv4Subnet()
	c.Assert(err, gc.ErrorMatches, "could not find unused subnet")
	c.Assert(subnet, gc.Equals, "")
}

func (s *InitialiserSuite) TestParseLXDBridgeFileValues(c *gc.C) {
	insignificantContent := `
# Comment 1, followed by empty line.

# Comment 2, followed by empty line.

  And a line that has content, but is not a comment, nor a key/value pair.
`
	for i, test := range []struct {
		desc     string
		content  string
		expected map[string]string
	}{{
		desc:     "empty content",
		content:  "",
		expected: map[string]string{},
	}, {
		desc:     "only comments and empty lines",
		content:  insignificantContent,
		expected: map[string]string{},
	}, {
		desc:     "missing key",
		content:  "=a",
		expected: map[string]string{},
	}, {
		desc:    "empty value",
		content: "a=",
		expected: map[string]string{
			"a": "",
		},
	}, {
		desc:    "value defined, but empty",
		content: `a=""`,
		expected: map[string]string{
			"a": "",
		},
	}, {
		desc:    "multiple entries",
		content: "a=b\nc=d\ne=f",
		expected: map[string]string{
			"a": "b",
			"c": "d",
			"e": "f",
		},
	}, {
		desc:    "comment with leading whitespace",
		content: " #a=b\nc=d\ne=f",
		expected: map[string]string{
			"c": "d",
			"e": "f",
		},
	}, {
		desc:    "key/value pairs with leading and trailing whitespace",
		content: " a=b\n c=d \ne=f ",
		expected: map[string]string{
			"a": "b",
			"c": "d",
			"e": "f",
		},
	}} {
		c.Logf("test #%d - %s", i, test.desc)
		values := parseLXDBridgeConfigValues(test.content)
		c.Check(values, gc.DeepEquals, test.expected)
	}
}

func (s *InitialiserSuite) TestParseLXDBridgeFileValuesWithRealWorldContent(c *gc.C) {
	expected := map[string]string{
		"USE_LXD_BRIDGE":      "true",
		"EXISTING_BRIDGE":     "",
		"LXD_BRIDGE":          "lxdbr0",
		"LXD_CONFILE":         "",
		"LXD_DOMAIN":          "lxd",
		"LXD_IPV4_ADDR":       "10.0.4.1",
		"LXD_IPV4_NETMASK":    "255.255.255.0",
		"LXD_IPV4_NETWORK":    "10.0.4.1/24",
		"LXD_IPV4_DHCP_RANGE": "10.0.4.2,10.0.4.100",
		"LXD_IPV4_DHCP_MAX":   "50",
		"LXD_IPV4_NAT":        "true",
		"LXD_IPV6_ADDR":       "2001:470:b2b5:9999::1",
		"LXD_IPV6_MASK":       "64",
		"LXD_IPV6_NETWORK":    "2001:470:b2b5:9999::1/64",
		"LXD_IPV6_NAT":        "true",
		"LXD_IPV6_PROXY":      "true",
	}
	values := parseLXDBridgeConfigValues(lxdBridgeContent)
	c.Check(values, gc.DeepEquals, expected)
}

func (s *InitialiserSuite) TestBridgeConfigurationWithNoChangeRequired(c *gc.C) {
	result, err := bridgeConfiguration(lxdBridgeContent)
	c.Assert(err, gc.IsNil)
	c.Assert(lxdBridgeContent, gc.Equals, result)
}

func (s *InitialiserSuite) TestBridgeConfigurationWithInterfacesError(c *gc.C) {
	s.PatchValue(&interfaceAddrs, func() ([]net.Addr, error) {
		return nil, errors.New("boom!")
	})
	result, err := bridgeConfiguration("")
	c.Assert(err, gc.ErrorMatches, "cannot get network interface addresses: boom!")
	c.Assert(result, gc.Equals, "")
}

func (s *InitialiserSuite) TestBridgeConfigurationWithNewSubnet(c *gc.C) {
	s.PatchValue(&interfaceAddrs, func() ([]net.Addr, error) {
		return testAddresses(c, "10.0.4.1/24")
	})

	expectedValues := map[string]string{
		"USE_LXD_BRIDGE":      "true",
		"EXISTING_BRIDGE":     "",
		"LXD_BRIDGE":          "lxdbr0",
		"LXD_IPV4_ADDR":       "10.0.5.1",
		"LXD_IPV4_NETMASK":    "255.255.255.0",
		"LXD_IPV4_NETWORK":    "10.0.5.1/24",
		"LXD_IPV4_DHCP_RANGE": "10.0.5.2,10.0.5.254",
		"LXD_IPV4_DHCP_MAX":   "253",
		"LXD_IPV4_NAT":        "true",
		"LXD_IPV6_PROXY":      "false",
	}

	result, err := bridgeConfiguration(`LXD_IPV4_ADDR=""`)
	c.Assert(err, gc.IsNil)
	actualValues := parseLXDBridgeConfigValues(result)
	c.Assert(expectedValues, gc.DeepEquals, actualValues)
}
