package ovn

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/openvswitch/ovn-kubernetes/go-controller/pkg/config"
	"github.com/openvswitch/ovn-kubernetes/go-controller/pkg/util"
	"github.com/sirupsen/logrus"
)

const (
	windowsOS = "windows"
)

func configureManagementPortWindows(clusterSubnet []string, clusterServicesSubnet,
	routerIP, interfaceName, interfaceIP string) error {
	// Up the interface.
	_, _, err := util.RunPowershell("Enable-NetAdapter", "-IncludeHidden", interfaceName)
	if err != nil {
		return err
	}

	//check if interface already exists
	ifAlias := fmt.Sprintf("-InterfaceAlias %s", interfaceName)
	_, _, err = util.RunPowershell("Get-NetIPAddress", ifAlias)
	if err == nil {
		//The interface already exists, we should delete the routes and IP
		logrus.Debugf("Interface %s exists, removing.", interfaceName)
		_, _, err = util.RunPowershell("Remove-NetIPAddress", ifAlias, "-Confirm:$false")
		if err != nil {
			return err
		}
	}

	// Assign IP address to the internal interface.
	portIP, interfaceIPNet, err := net.ParseCIDR(interfaceIP)
	if err != nil {
		return fmt.Errorf("Failed to parse interfaceIP %v : %v", interfaceIP, err)
	}
	portPrefix, _ := interfaceIPNet.Mask.Size()
	_, _, err = util.RunPowershell("New-NetIPAddress",
		fmt.Sprintf("-IPAddress %s", portIP),
		fmt.Sprintf("-PrefixLength %d", portPrefix),
		ifAlias)
	if err != nil {
		return err
	}

	// Set MTU for the interface
	_, _, err = util.RunNetsh("interface", "ipv4", "set", "subinterface",
		interfaceName, fmt.Sprintf("mtu=%d", config.Default.MTU),
		"store=persistent")
	if err != nil {
		return err
	}

	// Retrieve the interface index
	stdout, stderr, err := util.RunPowershell("$(Get-NetAdapter", "-IncludeHidden", "|", "Where",
		"{", "$_.Name", "-Match", fmt.Sprintf("\"%s\"", interfaceName), "}).ifIndex")
	if err != nil {
		logrus.Errorf("Failed to fetch interface index, stderr: %q, error: %v", stderr, err)
		return err
	}
	if _, err := strconv.Atoi(stdout); err != nil {
		logrus.Errorf("Failed to parse interface index %q: %v", stdout, err)
		return err
	}
	interfaceIndex := stdout

	for _, subnet := range clusterSubnet {
		subnetIP, subnetIPNet, err := net.ParseCIDR(subnet)
		if err != nil {
			return fmt.Errorf("failed to parse clusterSubnet %v : %v", subnet, err)
		}
		// Checking if the route already exists, in which case it will not be created again
		stdout, stderr, err = util.RunRoute("print", "-4", subnetIP.String())
		if err != nil {
			logrus.Debugf("Failed to run route print, stderr: %q, error: %v", stderr, err)
		}

		if strings.Contains(stdout, subnetIP.String()) {
			logrus.Debugf("Route was found, skipping route add")
		} else {
			// Windows route command requires the mask to be specified in the IP format
			subnetMask := net.IP(subnetIPNet.Mask).String()
			// Create a route for the entire subnet.
			_, stderr, err = util.RunRoute("-p", "add",
				subnetIP.String(), "mask", subnetMask,
				routerIP, "METRIC", "2", "IF", interfaceIndex)
			if err != nil {
				logrus.Errorf("failed to run route add, stderr: %q, error: %v", stderr, err)
				return err
			}
		}
	}

	if clusterServicesSubnet != "" {
		clusterServiceIP, clusterServiceIPNet, err := net.ParseCIDR(clusterServicesSubnet)
		if err != nil {
			return fmt.Errorf("Failed to parse clusterServicesSubnet %v : %v", clusterServicesSubnet, err)
		}
		// Checking if the route already exists, in which case it will not be created again
		stdout, stderr, err = util.RunRoute("print", "-4", clusterServiceIP.String())
		if err != nil {
			logrus.Debugf("Failed to run route print, stderr: %q, error: %v", stderr, err)
		}

		if strings.Contains(stdout, clusterServiceIP.String()) {
			logrus.Debugf("Route was found, skipping route add")
		} else {
			// Windows route command requires the mask to be specified in the IP format
			clusterServiceMask := net.IP(clusterServiceIPNet.Mask).String()
			// Create a route for the entire subnet.
			_, stderr, err = util.RunRoute("-p", "add",
				clusterServiceIP.String(), "mask", clusterServiceMask,
				routerIP, "METRIC", "2", "IF", interfaceIndex)
			if err != nil {
				logrus.Errorf("failed to run route add, stderr: %q, error: %v", stderr, err)
				return err
			}
		}
	}

	return nil
}

func configureManagementPort(clusterSubnet []string, clusterServicesSubnet,
	routerIP, routerMac, interfaceName, interfaceIP string) error {
	if runtime.GOOS == windowsOS {
		// Return here for Windows, the commands for enabling the interface, setting the IP and adding the
		// route will be done in the above function
		return configureManagementPortWindows(clusterSubnet, clusterServicesSubnet,
			routerIP, interfaceName, interfaceIP)
	}

	// Up the interface.
	_, _, err := util.RunIP("link", "set", interfaceName, "up")
	if err != nil {
		return err
	}

	// The interface may already exist, in which case delete the routes and IP.
	_, _, err = util.RunIP("addr", "flush", "dev", interfaceName)
	if err != nil {
		return err
	}

	// Assign IP address to the internal interface.
	_, _, err = util.RunIP("addr", "add", interfaceIP, "dev", interfaceName)
	if err != nil {
		return err
	}

	for _, subnet := range clusterSubnet {
		// Flush the route for the entire subnet (in case it was added before).
		_, _, err = util.RunIP("route", "flush", subnet)
		if err != nil {
			return err
		}

		// Create a route for the entire subnet.
		_, _, err = util.RunIP("route", "add", subnet, "via", routerIP)
		if err != nil {
			return err
		}
	}

	if clusterServicesSubnet != "" {
		// Flush the route for the services subnet (in case it was added before).
		_, _, err = util.RunIP("route", "flush", clusterServicesSubnet)
		if err != nil {
			return err
		}

		// Create a route for the services subnet.
		_, _, err = util.RunIP("route", "add", clusterServicesSubnet,
			"via", routerIP)
		if err != nil {
			return err
		}
	}

	// Add a neighbour entry on the K8s node to map routerIP with routerMAC. This is
	// required because in certain cases ARP requests from the K8s Node to the routerIP
	// arrives on OVN Logical Router pipeline with ARP source protocol address set to
	// K8s Node IP. OVN Logical Router pipeline drops such packets since it expects
	// source protocol address to be in the Logical Switch's subnet.
	_, _, err = util.RunIP("neigh", "add", routerIP, "dev", interfaceName, "lladdr", routerMac)
	if err != nil && os.IsNotExist(err) {
		return err
	}

	return nil
}

// CreateManagementPort creates a management port attached to the node switch
// that lets the node access its pods via their private IP address. This is used
// for health checking and other management tasks.
func CreateManagementPort(nodeName, localSubnet,
	clusterServicesSubnet string, clusterSubnet []string) error {

	// Determine the IP of the node switch's logical router port on the cluster router
	ip, subnet, err := net.ParseCIDR(localSubnet)
	if err != nil {
		return fmt.Errorf("Failed to parse local subnet %s: %v", localSubnet, err)
	}
	ip = util.NextIP(ip)
	routerIP := ip.String()

	// Kubernetes emits events when pods are created. The event will contain
	// only lowercase letters of the hostname even though the kubelet is
	// started with a hostname that contains lowercase and uppercase letters.
	// When the kubelet is started with a hostname containing lowercase and
	// uppercase letters, this causes a mismatch between what the watcher
	// will try to fetch and what kubernetes provides, thus failing to
	// create the port on the logical switch.
	// Until the above is changed, switch to a lowercase hostname for
	// initMinion.
	nodeName = strings.ToLower(nodeName)

	// Make sure br-int is created.
	stdout, stderr, err := util.RunOVSVsctl("--", "--may-exist", "add-br", "br-int")
	if err != nil {
		logrus.Errorf("Failed to create br-int, stdout: %q, stderr: %q, error: %v", stdout, stderr, err)
		return err
	}

	// Create a OVS internal interface.
	interfaceName := util.GetK8sMgmtIntfName(nodeName)

	stdout, stderr, err = util.RunOVSVsctl("--", "--may-exist", "add-port",
		"br-int", interfaceName, "--", "set", "interface", interfaceName,
		"type=internal", "mtu_request="+fmt.Sprintf("%d", config.Default.MTU),
		"external-ids:iface-id=k8s-"+nodeName)
	if err != nil {
		logrus.Errorf("Failed to add port to br-int, stdout: %q, stderr: %q, error: %v", stdout, stderr, err)
		return err
	}
	macAddress, stderr, err := util.RunOVSVsctl("--if-exists", "get", "interface", interfaceName, "mac_in_use")
	if err != nil {
		logrus.Errorf("Failed to get mac address of %v, stderr: %q, error: %v", interfaceName, stderr, err)
		return err
	}
	if macAddress == "[]" {
		return fmt.Errorf("Failed to get mac address of %v", interfaceName)
	}

	if runtime.GOOS == windowsOS && macAddress == "00:00:00:00:00:00" {
		macAddress, err = util.FetchIfMacWindows(interfaceName)
		if err != nil {
			return err
		}
	}

	// Create this node's management logical port on the node switch
	ip = util.NextIP(ip)
	portIP := ip.String()
	n, _ := subnet.Mask.Size()
	portIPMask := fmt.Sprintf("%s/%d", portIP, n)
	stdout, stderr, err = util.RunOVNNbctl("--", "--may-exist", "lsp-add", nodeName, "k8s-"+nodeName, "--", "lsp-set-addresses", "k8s-"+nodeName, macAddress+" "+portIP)
	if err != nil {
		logrus.Errorf("Failed to add logical port to switch, stdout: %q, stderr: %q, error: %v", stdout, stderr, err)
		return err
	}
	// switch-to-router ports only have MAC address and nothing else.
	routerMac, stderr, err := util.RunOVNNbctl("lsp-get-addresses", "stor-"+nodeName)
	if err != nil {
		logrus.Errorf("Failed to retrieve the MAC address of the logical port, stderr: %q, error: %v",
			stderr, err)
		return err
	}
	err = configureManagementPort(clusterSubnet, clusterServicesSubnet,
		routerIP, routerMac, interfaceName, portIPMask)
	return err
}
