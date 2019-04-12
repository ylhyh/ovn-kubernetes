package config

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli"
	kexec "k8s.io/utils/exec"
	fakeexec "k8s.io/utils/exec/testing"

	ovntest "github.com/openvswitch/ovn-kubernetes/go-controller/pkg/testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

func TestConfig(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Config Suite")
}

func writeConfigFile(cfgFile *os.File, randomOptData bool, args ...string) error {
	// Convert command-line args into sections and options
	sections := make(map[string][]string)
	for _, arg := range args {
		var section string
		switch {
		case strings.HasPrefix(arg, "-k8s-"):
			section = "kubernetes"
			arg = arg[5:]
		case strings.HasPrefix(arg, "-cni-"):
			section = "cni"
			arg = arg[5:]
		case strings.HasPrefix(arg, "-log"):
			section = "logging"
			arg = arg[1:]
		case strings.HasPrefix(arg, "-conntrack-zone"):
			section = "defaults"
			arg = arg[1:]
		case strings.HasPrefix(arg, "-mtu"):
			section = "defaults"
			arg = arg[1:]
		case strings.HasPrefix(arg, "-nb-"):
			section = "ovnnorth"
			arg = arg[4:]
		case strings.HasPrefix(arg, "-sb-"):
			section = "ovnsouth"
			arg = arg[4:]
		default:
			panic("shouldn't get here")
		}

		if randomOptData {
			parts := strings.Split(arg, "=")
			Expect(len(parts)).To(Equal(2))
			sections[section] = append(sections[section], parts[0]+"=aklsdjfalsdfkjaslfdkjasfdlksa")
		} else {
			sections[section] = append(sections[section], arg)
		}
	}

	// Write sections and options to the file data
	var data string
	for k, array := range sections {
		data += fmt.Sprintf("[%s]\n", k)
		for _, item := range array {
			data += item + "\n"
		}
	}

	_, err := cfgFile.Write([]byte(data))
	return err
}

// runType 1: command-line args only
// runType 2: config file only
// runType 3: command-line args and random config file option data to test CLI override
func runInit(app *cli.App, runType int, cfgFile *os.File, args ...string) error {
	app.Action = func(ctx *cli.Context) error {
		_, err := InitConfig(ctx, kexec.New(), nil)
		return err
	}

	finalArgs := []string{app.Name, "-loglevel=5"}
	switch runType {
	case 1:
		finalArgs = append(finalArgs, args...)
	case 2:
		finalArgs = append(finalArgs, "-config-file="+cfgFile.Name())
		if err := writeConfigFile(cfgFile, false, args...); err != nil {
			return err
		}
	case 3:
		finalArgs = append(finalArgs, "-config-file="+cfgFile.Name())
		finalArgs = append(finalArgs, args...)
		if err := writeConfigFile(cfgFile, true, args...); err != nil {
			return err
		}
	default:
		panic("shouldn't get here")
	}
	return app.Run(finalArgs)
}

var tmpDir string

var _ = AfterSuite(func() {
	err := os.RemoveAll(tmpDir)
	Expect(err).NotTo(HaveOccurred())
})

func createTempFile(name string) (string, error) {
	fname := filepath.Join(tmpDir, name)
	if err := ioutil.WriteFile(fname, []byte{0x20}, 0644); err != nil {
		return "", err
	}
	return fname, nil
}

var _ = Describe("Config Operations", func() {
	var app *cli.App
	var cfgFile *os.File

	var tmpErr error
	tmpDir, tmpErr = ioutil.TempDir("", "configtest_certdir")
	if tmpErr != nil {
		GinkgoT().Errorf("failed to create tempdir: %v", tmpErr)
	}

	BeforeEach(func() {
		// Restore global default values before each testcase
		RestoreDefaultConfig()

		app = cli.NewApp()
		app.Name = "test"
		app.Flags = Flags

		var err error
		cfgFile, err = ioutil.TempFile("", "conftest-")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		os.Remove(cfgFile.Name())
	})

	It("uses expected defaults", func() {
		app.Action = func(ctx *cli.Context) error {
			cfgPath, err := InitConfig(ctx, kexec.New(), nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfgPath).To(Equal(cfgFile.Name()))

			Expect(Default.MTU).To(Equal(1400))
			Expect(Default.ConntrackZone).To(Equal(64000))
			Expect(Logging.File).To(Equal(""))
			Expect(Logging.Level).To(Equal(4))
			Expect(CNI.ConfDir).To(Equal("/etc/cni/net.d"))
			Expect(CNI.Plugin).To(Equal("ovn-k8s-cni-overlay"))
			Expect(Kubernetes.Kubeconfig).To(Equal(""))
			Expect(Kubernetes.CACert).To(Equal(""))
			Expect(Kubernetes.Token).To(Equal(""))
			Expect(Kubernetes.APIServer).To(Equal("http://localhost:8080"))

			for _, a := range []*OvnDBAuth{OvnNorth.ClientAuth, OvnSouth.ClientAuth} {
				Expect(a.Scheme).To(Equal(OvnDBSchemeUnix))
				Expect(a.PrivKey).To(Equal(""))
				Expect(a.Cert).To(Equal(""))
				Expect(a.CACert).To(Equal(""))
				Expect(a.OvnAddressForClient).To(Equal(""))
			}
			return nil
		}
		err := app.Run([]string{app.Name, "-config-file=" + cfgFile.Name()})
		Expect(err).NotTo(HaveOccurred())
	})

	It("reads defaults from ovs-vsctl external IDs", func() {
		app.Action = func(ctx *cli.Context) error {
			// k8s-api-server
			fakeCmds := ovntest.AddFakeCmd(nil, &ovntest.ExpectedCmd{
				Cmd:    "ovs-vsctl --timeout=15 --if-exists get Open_vSwitch . external_ids:k8s-api-server",
				Output: "https://somewhere.com:8081",
			})

			// k8s-api-token
			fakeCmds = ovntest.AddFakeCmd(fakeCmds, &ovntest.ExpectedCmd{
				Cmd:    "ovs-vsctl --timeout=15 --if-exists get Open_vSwitch . external_ids:k8s-api-token",
				Output: "asadfasdfasrw3atr3r3rf33fasdaa3233",
			})
			// k8s-ca-certificate
			fname, err := createTempFile("kube-cacert.pem")
			Expect(err).NotTo(HaveOccurred())
			fakeCmds = ovntest.AddFakeCmd(fakeCmds, &ovntest.ExpectedCmd{
				Cmd:    "ovs-vsctl --timeout=15 --if-exists get Open_vSwitch . external_ids:k8s-ca-certificate",
				Output: fname,
			})
			// ovn-nb address
			fakeCmds = ovntest.AddFakeCmd(fakeCmds, &ovntest.ExpectedCmd{
				Cmd:    "ovs-vsctl --timeout=15 --if-exists get Open_vSwitch . external_ids:ovn-nb",
				Output: "tcp:1.1.1.1:6441",
			})

			fexec := &fakeexec.FakeExec{
				CommandScript: fakeCmds,
				LookPathFunc: func(file string) (string, error) {
					return fmt.Sprintf("/fake-bin/%s", file), nil
				},
			}

			cfgPath, err := InitConfig(ctx, fexec, &Defaults{
				OvnNorthAddress: true,
				K8sAPIServer:    true,
				K8sToken:        true,
				K8sCert:         true,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(cfgPath).To(Equal(cfgFile.Name()))
			Expect(fexec.CommandCalls).To(Equal(len(fakeCmds)))

			Expect(Kubernetes.APIServer).To(Equal("https://somewhere.com:8081"))
			Expect(Kubernetes.CACert).To(Equal(fname))
			Expect(Kubernetes.Token).To(Equal("asadfasdfasrw3atr3r3rf33fasdaa3233"))

			Expect(OvnNorth.ClientAuth.Scheme).To(Equal(OvnDBSchemeTCP))
			Expect(OvnNorth.ClientAuth.PrivKey).To(Equal(""))
			Expect(OvnNorth.ClientAuth.Cert).To(Equal(""))
			Expect(OvnNorth.ClientAuth.CACert).To(Equal(""))
			Expect(OvnNorth.ClientAuth.OvnAddressForClient).To(Equal("tcp:1.1.1.1:6441"))

			Expect(OvnSouth.ClientAuth.Scheme).To(Equal(OvnDBSchemeUnix))
			Expect(OvnSouth.ClientAuth.PrivKey).To(Equal(""))
			Expect(OvnSouth.ClientAuth.Cert).To(Equal(""))
			Expect(OvnSouth.ClientAuth.CACert).To(Equal(""))
			Expect(OvnSouth.ClientAuth.OvnAddressForClient).To(Equal(""))

			return nil
		}
		err := app.Run([]string{app.Name, "-config-file=" + cfgFile.Name()})
		Expect(err).NotTo(HaveOccurred())
	})

	It("reads defaults (multiple master) from ovs-vsctl external IDs", func() {
		app.Action = func(ctx *cli.Context) error {
			// k8s-api-server
			fakeCmds := ovntest.AddFakeCmd(nil, &ovntest.ExpectedCmd{
				Cmd:    "ovs-vsctl --timeout=15 --if-exists get Open_vSwitch . external_ids:k8s-api-server",
				Output: "https://somewhere.com:8081",
			})

			// k8s-api-token
			fakeCmds = ovntest.AddFakeCmd(fakeCmds, &ovntest.ExpectedCmd{
				Cmd:    "ovs-vsctl --timeout=15 --if-exists get Open_vSwitch . external_ids:k8s-api-token",
				Output: "asadfasdfasrw3atr3r3rf33fasdaa3233",
			})
			// k8s-ca-certificate
			fname, err := createTempFile("kube-cacert.pem")
			Expect(err).NotTo(HaveOccurred())
			fakeCmds = ovntest.AddFakeCmd(fakeCmds, &ovntest.ExpectedCmd{
				Cmd:    "ovs-vsctl --timeout=15 --if-exists get Open_vSwitch . external_ids:k8s-ca-certificate",
				Output: fname,
			})
			// ovn-nb address
			fakeCmds = ovntest.AddFakeCmd(fakeCmds, &ovntest.ExpectedCmd{
				Cmd:    "ovs-vsctl --timeout=15 --if-exists get Open_vSwitch . external_ids:ovn-nb",
				Output: "tcp:1.1.1.1:6441,tcp:1.1.1.2:6641,tcp:1.1.1.3:6641",
			})

			fexec := &fakeexec.FakeExec{
				CommandScript: fakeCmds,
				LookPathFunc: func(file string) (string, error) {
					return fmt.Sprintf("/fake-bin/%s", file), nil
				},
			}

			cfgPath, err := InitConfig(ctx, fexec, &Defaults{
				OvnNorthAddress: true,
				K8sAPIServer:    true,
				K8sToken:        true,
				K8sCert:         true,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(cfgPath).To(Equal(cfgFile.Name()))
			Expect(fexec.CommandCalls).To(Equal(len(fakeCmds)))

			Expect(Kubernetes.APIServer).To(Equal("https://somewhere.com:8081"))
			Expect(Kubernetes.CACert).To(Equal(fname))
			Expect(Kubernetes.Token).To(Equal("asadfasdfasrw3atr3r3rf33fasdaa3233"))

			Expect(OvnNorth.ClientAuth.Scheme).To(Equal(OvnDBSchemeTCP))
			Expect(OvnNorth.ClientAuth.PrivKey).To(Equal(""))
			Expect(OvnNorth.ClientAuth.Cert).To(Equal(""))
			Expect(OvnNorth.ClientAuth.CACert).To(Equal(""))
			Expect(OvnNorth.ClientAuth.OvnAddressForClient).To(
				Equal("tcp:1.1.1.1:6441,tcp:1.1.1.2:6641,tcp:1.1.1.3:6641"))

			Expect(OvnSouth.ClientAuth.Scheme).To(Equal(OvnDBSchemeUnix))
			Expect(OvnSouth.ClientAuth.PrivKey).To(Equal(""))
			Expect(OvnSouth.ClientAuth.Cert).To(Equal(""))
			Expect(OvnSouth.ClientAuth.CACert).To(Equal(""))
			Expect(OvnSouth.ClientAuth.OvnAddressForClient).To(Equal(""))

			return nil
		}
		err := app.Run([]string{app.Name, "-config-file=" + cfgFile.Name()})
		Expect(err).NotTo(HaveOccurred())
	})

	It("overrides defaults with config file options", func() {
		kubeconfigFile, err := createTempFile("kubeconfig")
		Expect(err).NotTo(HaveOccurred())
		defer os.Remove(kubeconfigFile)

		kubeCAFile, err := createTempFile("kube-ca.crt")
		Expect(err).NotTo(HaveOccurred())
		defer os.Remove(kubeCAFile)

		cfgData := fmt.Sprintf(`[default]
mtu=1500
conntrack-zone=64321

[kubernetes]
kubeconfig=%s
apiserver=https://1.2.3.4:6443
token=TG9yZW0gaXBzdW0gZ
cacert=%s

[logging]
loglevel=5
logfile=/var/log/ovnkube.log

[cni]
conf-dir=/etc/cni/net.d22
plugin=ovn-k8s-cni-overlay22

[ovnnorth]
address=ssl://1.2.3.4:6641
client-privkey=/path/to/nb-client-private.key
client-cert=/path/to/nb-client.crt
client-cacert=/path/to/nb-client-ca.crt

[ovnsouth]
address=ssl://1.2.3.4:6642
client-privkey=/path/to/sb-client-private.key
client-cert=/path/to/sb-client.crt
client-cacert=/path/to/sb-client-ca.crt
`, kubeconfigFile, kubeCAFile)
		err = ioutil.WriteFile(cfgFile.Name(), []byte(cfgData), 0644)
		Expect(err).NotTo(HaveOccurred())

		app.Action = func(ctx *cli.Context) error {
			var cfgPath string
			cfgPath, err = InitConfig(ctx, kexec.New(), nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfgPath).To(Equal(cfgFile.Name()))

			Expect(Default.MTU).To(Equal(1500))
			Expect(Default.ConntrackZone).To(Equal(64321))
			Expect(Logging.File).To(Equal("/var/log/ovnkube.log"))
			Expect(Logging.Level).To(Equal(5))
			Expect(CNI.ConfDir).To(Equal("/etc/cni/net.d22"))
			Expect(CNI.Plugin).To(Equal("ovn-k8s-cni-overlay22"))
			Expect(Kubernetes.Kubeconfig).To(Equal(kubeconfigFile))
			Expect(Kubernetes.CACert).To(Equal(kubeCAFile))
			Expect(Kubernetes.Token).To(Equal("TG9yZW0gaXBzdW0gZ"))
			Expect(Kubernetes.APIServer).To(Equal("https://1.2.3.4:6443"))

			Expect(OvnNorth.ClientAuth.Scheme).To(Equal(OvnDBSchemeSSL))
			Expect(OvnNorth.ClientAuth.PrivKey).To(Equal("/path/to/nb-client-private.key"))
			Expect(OvnNorth.ClientAuth.Cert).To(Equal("/path/to/nb-client.crt"))
			Expect(OvnNorth.ClientAuth.CACert).To(Equal("/path/to/nb-client-ca.crt"))
			Expect(OvnNorth.ClientAuth.OvnAddressForClient).To(Equal("ssl:1.2.3.4:6641"))

			Expect(OvnSouth.ClientAuth.Scheme).To(Equal(OvnDBSchemeSSL))
			Expect(OvnSouth.ClientAuth.PrivKey).To(Equal("/path/to/sb-client-private.key"))
			Expect(OvnSouth.ClientAuth.Cert).To(Equal("/path/to/sb-client.crt"))
			Expect(OvnSouth.ClientAuth.CACert).To(Equal("/path/to/sb-client-ca.crt"))
			Expect(OvnSouth.ClientAuth.OvnAddressForClient).To(Equal("ssl:1.2.3.4:6642"))

			return nil
		}
		err = app.Run([]string{app.Name, "-config-file=" + cfgFile.Name()})
		Expect(err).NotTo(HaveOccurred())
	})

	It("overrides config file and defaults with CLI options", func() {
		kubeconfigFile, err := createTempFile("kubeconfig")
		Expect(err).NotTo(HaveOccurred())
		defer os.Remove(kubeconfigFile)

		kubeCAFile, err := createTempFile("kube-ca.crt")
		Expect(err).NotTo(HaveOccurred())
		defer os.Remove(kubeCAFile)

		err = ioutil.WriteFile(cfgFile.Name(), []byte(`[default]
mtu=1500
conntrack-zone=64321

[kubernetes]
kubeconfig=/path/to/kubeconfig
apiserver=https://1.2.3.4:6443
token=TG9yZW0gaXBzdW0gZ
cacert=/path/to/kubeca.crt

[logging]
loglevel=5
logfile=/var/log/ovnkube.log

[cni]
conf-dir=/etc/cni/net.d22
plugin=ovn-k8s-cni-overlay22

[ovnnorth]
address=ssl://1.2.3.4:6641
client-privkey=/path/to/nb-client-private.key
client-cert=/path/to/nb-client.crt
client-cacert=/path/to/nb-client-ca.crt

[ovnsouth]
address=ssl://1.2.3.4:6642
client-privkey=/path/to/sb-client-private.key
client-cert=/path/to/sb-client.crt
client-cacert=/path/to/sb-client-ca.crt
`), 0644)
		Expect(err).NotTo(HaveOccurred())

		app.Action = func(ctx *cli.Context) error {
			var cfgPath string
			cfgPath, err = InitConfig(ctx, kexec.New(), nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfgPath).To(Equal(cfgFile.Name()))

			Expect(Default.MTU).To(Equal(1234))
			Expect(Default.ConntrackZone).To(Equal(5555))
			Expect(Logging.File).To(Equal("/some/logfile"))
			Expect(Logging.Level).To(Equal(3))
			Expect(CNI.ConfDir).To(Equal("/some/cni/dir"))
			Expect(CNI.Plugin).To(Equal("a-plugin"))
			Expect(Kubernetes.Kubeconfig).To(Equal(kubeconfigFile))
			Expect(Kubernetes.CACert).To(Equal(kubeCAFile))
			Expect(Kubernetes.Token).To(Equal("asdfasdfasdfasfd"))
			Expect(Kubernetes.APIServer).To(Equal("https://4.4.3.2:8080"))

			Expect(OvnNorth.ClientAuth.Scheme).To(Equal(OvnDBSchemeSSL))
			Expect(OvnNorth.ClientAuth.PrivKey).To(Equal("/client/privkey"))
			Expect(OvnNorth.ClientAuth.Cert).To(Equal("/client/cert"))
			Expect(OvnNorth.ClientAuth.CACert).To(Equal("/client/cacert"))
			Expect(OvnNorth.ClientAuth.OvnAddressForClient).To(Equal("ssl:6.5.4.3:6651"))

			Expect(OvnSouth.ClientAuth.Scheme).To(Equal(OvnDBSchemeSSL))
			Expect(OvnSouth.ClientAuth.PrivKey).To(Equal("/client/privkey2"))
			Expect(OvnSouth.ClientAuth.Cert).To(Equal("/client/cert2"))
			Expect(OvnSouth.ClientAuth.CACert).To(Equal("/client/cacert2"))
			Expect(OvnSouth.ClientAuth.OvnAddressForClient).To(Equal("ssl:6.5.4.1:6652"))

			return nil
		}
		cliArgs := []string{
			app.Name,
			"-config-file=" + cfgFile.Name(),
			"-mtu=1234",
			"-conntrack-zone=5555",
			"-loglevel=3",
			"-logfile=/some/logfile",
			"-cni-conf-dir=/some/cni/dir",
			"-cni-plugin=a-plugin",
			"-k8s-kubeconfig=" + kubeconfigFile,
			"-k8s-apiserver=https://4.4.3.2:8080",
			"-k8s-cacert=" + kubeCAFile,
			"-k8s-token=asdfasdfasdfasfd",
			"-nb-address=ssl://6.5.4.3:6651",
			"-nb-client-privkey=/client/privkey",
			"-nb-client-cert=/client/cert",
			"-nb-client-cacert=/client/cacert",
			"-sb-address=ssl://6.5.4.1:6652",
			"-sb-client-privkey=/client/privkey2",
			"-sb-client-cert=/client/cert2",
			"-sb-client-cacert=/client/cacert2",
		}
		err = app.Run(cliArgs)
		Expect(err).NotTo(HaveOccurred())
	})

	It("overrides config file and defaults with CLI options (multi-master)", func() {
		kubeconfigFile, err := createTempFile("kubeconfig")
		Expect(err).NotTo(HaveOccurred())
		defer os.Remove(kubeconfigFile)

		kubeCAFile, err := createTempFile("kube-ca.crt")
		Expect(err).NotTo(HaveOccurred())
		defer os.Remove(kubeCAFile)

		err = ioutil.WriteFile(cfgFile.Name(), []byte(`[default]
mtu=1500
conntrack-zone=64321

[kubernetes]
kubeconfig=/path/to/kubeconfig
apiserver=https://1.2.3.4:6443
token=TG9yZW0gaXBzdW0gZ
cacert=/path/to/kubeca.crt

[logging]
loglevel=5
logfile=/var/log/ovnkube.log

[cni]
conf-dir=/etc/cni/net.d22
plugin=ovn-k8s-cni-overlay22

[ovnnorth]
address=ssl://1.2.3.4:6641
client-privkey=/path/to/nb-client-private.key
client-cert=/path/to/nb-client.crt
client-cacert=/path/to/nb-client-ca.crt

[ovnsouth]
address=ssl://1.2.3.4:6642
client-privkey=/path/to/sb-client-private.key
client-cert=/path/to/sb-client.crt
client-cacert=/path/to/sb-client-ca.crt
`), 0644)
		Expect(err).NotTo(HaveOccurred())

		app.Action = func(ctx *cli.Context) error {
			var cfgPath string
			cfgPath, err = InitConfig(ctx, kexec.New(), nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfgPath).To(Equal(cfgFile.Name()))

			Expect(Default.MTU).To(Equal(1234))
			Expect(Default.ConntrackZone).To(Equal(5555))
			Expect(Logging.File).To(Equal("/some/logfile"))
			Expect(Logging.Level).To(Equal(3))
			Expect(CNI.ConfDir).To(Equal("/some/cni/dir"))
			Expect(CNI.Plugin).To(Equal("a-plugin"))
			Expect(Kubernetes.Kubeconfig).To(Equal(kubeconfigFile))
			Expect(Kubernetes.CACert).To(Equal(kubeCAFile))
			Expect(Kubernetes.Token).To(Equal("asdfasdfasdfasfd"))
			Expect(Kubernetes.APIServer).To(Equal("https://4.4.3.2:8080"))

			Expect(OvnNorth.ClientAuth.Scheme).To(Equal(OvnDBSchemeSSL))
			Expect(OvnNorth.ClientAuth.PrivKey).To(Equal("/client/privkey"))
			Expect(OvnNorth.ClientAuth.Cert).To(Equal("/client/cert"))
			Expect(OvnNorth.ClientAuth.CACert).To(Equal("/client/cacert"))
			Expect(OvnNorth.ClientAuth.OvnAddressForClient).To(
				Equal("ssl:6.5.4.3:6651,ssl:6.5.4.4:6651,ssl:6.5.4.5:6651"))

			Expect(OvnSouth.ClientAuth.Scheme).To(Equal(OvnDBSchemeSSL))
			Expect(OvnSouth.ClientAuth.PrivKey).To(Equal("/client/privkey2"))
			Expect(OvnSouth.ClientAuth.Cert).To(Equal("/client/cert2"))
			Expect(OvnSouth.ClientAuth.CACert).To(Equal("/client/cacert2"))
			Expect(OvnSouth.ClientAuth.OvnAddressForClient).To(
				Equal("ssl:6.5.4.1:6652,ssl:6.5.4.2:6652,ssl:6.5.4.3:6652"))

			return nil
		}
		cliArgs := []string{
			app.Name,
			"-config-file=" + cfgFile.Name(),
			"-mtu=1234",
			"-conntrack-zone=5555",
			"-loglevel=3",
			"-logfile=/some/logfile",
			"-cni-conf-dir=/some/cni/dir",
			"-cni-plugin=a-plugin",
			"-k8s-kubeconfig=" + kubeconfigFile,
			"-k8s-apiserver=https://4.4.3.2:8080",
			"-k8s-cacert=" + kubeCAFile,
			"-k8s-token=asdfasdfasdfasfd",
			"-nb-address=ssl://6.5.4.3:6651,ssl://6.5.4.4:6651,ssl://6.5.4.5:6651",
			"-nb-client-privkey=/client/privkey",
			"-nb-client-cert=/client/cert",
			"-nb-client-cacert=/client/cacert",
			"-sb-address=ssl://6.5.4.1:6652,ssl://6.5.4.2:6652,ssl://6.5.4.3:6652",
			"-sb-client-privkey=/client/privkey2",
			"-sb-client-cert=/client/cert2",
			"-sb-client-cacert=/client/cacert2",
		}
		err = app.Run(cliArgs)
		Expect(err).NotTo(HaveOccurred())
	})

	Describe("OvnDBAuth operations", func() {
		var certFile, keyFile, caFile string

		BeforeEach(func() {
			var err error
			certFile, err = createTempFile("cert.crt")
			Expect(err).NotTo(HaveOccurred())
			keyFile, err = createTempFile("priv.key")
			Expect(err).NotTo(HaveOccurred())
			caFile = filepath.Join(tmpDir, "ca.crt")
		})

		AfterEach(func() {
			err := os.Remove(certFile)
			Expect(err).NotTo(HaveOccurred())
			err = os.Remove(keyFile)
			Expect(err).NotTo(HaveOccurred())
			os.Remove(caFile)
		})

		const (
			nbURL string = "ssl://1.2.3.4:6641"
			sbURL string = "ssl://1.2.3.4:6642"
		)

		It("configures client northbound SSL correctly", func() {
			const nbURLOVN string = "ssl:1.2.3.4:6641"

			fakeCmds := ovntest.AddFakeCmd(nil, &ovntest.ExpectedCmd{
				Cmd: fmt.Sprintf("ovn-nbctl --db=%s --timeout=5 --private-key=%s --certificate=%s --bootstrap-ca-cert=%s list nb_global", nbURLOVN, keyFile, certFile, caFile),
			})
			fakeCmds = ovntest.AddFakeCmd(fakeCmds, &ovntest.ExpectedCmd{
				Cmd: fmt.Sprintf("ovs-vsctl --timeout=15 set Open_vSwitch . external_ids:ovn-nb=\"%s\"", nbURLOVN),
			})

			fexec := &fakeexec.FakeExec{
				CommandScript: fakeCmds,
				LookPathFunc: func(file string) (string, error) {
					return fmt.Sprintf("/fake-bin/%s", file), nil
				},
			}

			a, err := newOvnDBAuth(fexec, "ovn-nbctl", "ovn-nb", nbURL, keyFile, certFile, caFile)
			Expect(err).NotTo(HaveOccurred())
			Expect(a.Scheme).To(Equal(OvnDBSchemeSSL))
			Expect(a.PrivKey).To(Equal(keyFile))
			Expect(a.Cert).To(Equal(certFile))
			Expect(a.CACert).To(Equal(caFile))
			Expect(a.OvnAddressForClient).To(Equal("ssl:1.2.3.4:6641"))
			Expect(a.ctlCmd).To(Equal("ovn-nbctl"))
			Expect(a.externalID).To(Equal("ovn-nb"))

			Expect(a.GetURL()).To(Equal(nbURLOVN))
			err = a.SetDBAuth()
			Expect(err).NotTo(HaveOccurred())
			Expect(fexec.CommandCalls).To(Equal(len(fakeCmds)))
		})

		It("configures client southbound SSL correctly", func() {
			const sbURLOVN string = "ssl:1.2.3.4:6642"

			fakeCmds := ovntest.AddFakeCmd(nil, &ovntest.ExpectedCmd{
				Cmd: fmt.Sprintf("ovn-nbctl --db=%s --timeout=5 --private-key=%s --certificate=%s --bootstrap-ca-cert=%s list nb_global", sbURLOVN, keyFile, certFile, caFile),
			})
			fakeCmds = ovntest.AddFakeCmd(fakeCmds, &ovntest.ExpectedCmd{
				Cmd: "ovs-vsctl --timeout=15 del-ssl",
			})
			fakeCmds = ovntest.AddFakeCmd(fakeCmds, &ovntest.ExpectedCmd{
				Cmd: fmt.Sprintf("ovs-vsctl --timeout=15 set-ssl %s %s %s", keyFile, certFile, caFile),
			})
			fakeCmds = ovntest.AddFakeCmd(fakeCmds, &ovntest.ExpectedCmd{
				Cmd: fmt.Sprintf("ovs-vsctl --timeout=15 set Open_vSwitch . external_ids:ovn-remote=\"%s\"", sbURLOVN),
			})

			fexec := &fakeexec.FakeExec{
				CommandScript: fakeCmds,
				LookPathFunc: func(file string) (string, error) {
					return fmt.Sprintf("/fake-bin/%s", file), nil
				},
			}

			a, err := newOvnDBAuth(fexec, "ovn-sbctl", "ovn-remote", sbURL, keyFile, certFile, caFile)
			Expect(err).NotTo(HaveOccurred())
			Expect(a.Scheme).To(Equal(OvnDBSchemeSSL))
			Expect(a.PrivKey).To(Equal(keyFile))
			Expect(a.Cert).To(Equal(certFile))
			Expect(a.CACert).To(Equal(caFile))
			Expect(a.OvnAddressForClient).To(Equal("ssl:1.2.3.4:6642"))
			Expect(a.ctlCmd).To(Equal("ovn-sbctl"))
			Expect(a.externalID).To(Equal("ovn-remote"))

			Expect(a.GetURL()).To(Equal(sbURLOVN))
			err = a.SetDBAuth()
			Expect(err).NotTo(HaveOccurred())
			Expect(fexec.CommandCalls).To(Equal(len(fakeCmds)))
		})
	})

	// This testcase factory function exists only to ensure that 'runType'
	// and 'dir' are evaluated when this factory function is called (and
	// the It() is created), but that the CLI arguments are evaluated only
	// when the test function is actually executed.
	createOneTest := func(runType int, dir, match string, getArgs func() []string) func() {
		return func() {
			args := getArgs()
			finalArgs := make([]string, len(args))
			if dir == "" {
				finalArgs = args
			} else {
				// Update args for OVN NB/SB database options
				for i, a := range args {
					finalArgs[i] = fmt.Sprintf("-%s-%s", dir, a)
				}
			}
			err := runInit(app, runType, cfgFile, finalArgs...)
			if match != "" {
				Expect(err).To(MatchError(match))
			} else {
				Expect(err).NotTo(HaveOccurred())
			}
		}
	}

	// Generates multiple runType and direction It() tests for a given description, match, and args
	generateTests := func(desc, match string, getArgs func() []string) {
		for _, dir := range []string{"nb", "sb"} {
			for runType := 1; runType <= 3; runType++ {
				realDesc := fmt.Sprintf("(%d/%s) %s", runType, dir, desc)
				It(realDesc, createOneTest(runType, dir, match, getArgs))
			}
		}
	}

	// Generates multiple runType It() tests for a given description, match, and args
	generateTestsSimple := func(desc, match string, args ...string) {
		for runType := 1; runType <= 3; runType++ {
			realDesc := fmt.Sprintf("(%d) %s", runType, desc)
			It(realDesc, createOneTest(runType, "", match, func() []string {
				return args
			}))
		}
	}

	// Run once without config file, once with
	Describe("Kubernetes config options", func() {
		Context("returns an error when the", func() {
			generateTestsSimple("CA cert does not exist",
				"kubernetes CA certificate file \"/foo/bar/baz.cert\" not found",
				"-k8s-apiserver=https://localhost:8443", "-k8s-cacert=/foo/bar/baz.cert")

			generateTestsSimple("apiserver URL scheme is invalid",
				"kubernetes API server URL scheme \"gggggg\" invalid",
				"-k8s-apiserver=gggggg://localhost:8443")

			generateTestsSimple("apiserver URL is invalid",
				"kubernetes API server address \"http://a b.com/\" invalid: parse http://a b.com/: invalid character \" \" in host name",
				"-k8s-apiserver=http://a b.com/")

			generateTestsSimple("kubeconfig file does not exist",
				"kubernetes kubeconfig file \"/foo/bar/baz\" not found",
				"-k8s-kubeconfig=/foo/bar/baz")
		})
	})

	Describe("OVN API config options", func() {
		var certFile, keyFile, caFile string

		BeforeEach(func() {
			var err error
			certFile, err = createTempFile("cert.crt")
			Expect(err).NotTo(HaveOccurred())
			keyFile, err = createTempFile("priv.key")
			Expect(err).NotTo(HaveOccurred())
			caFile, err = createTempFile("ca.crt")
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			os.Remove(certFile)
			os.Remove(keyFile)
			os.Remove(caFile)
		})

		Context("returns an error when", func() {
			generateTests("the scheme is not empty/tcp/ssl",
				"unknown OVN DB scheme \"blah\"",
				func() []string {
					return []string{"address=blah://1.2.3.4:5555"}
				})

			generateTests("the address is unix socket and certs are given",
				"certificate or key given; perhaps you mean to use the 'ssl' scheme?",
				func() []string {
					return []string{
						"client-privkey=/bar/baz/foo",
						"client-cert=/bar/baz/foo",
						"client-cacert=/var/baz/foo",
					}
				})

			generateTests("the OVN URL has no port",
				"Failed to parse OVN address tcp:4.3.2.1",
				func() []string {
					return []string{
						"address=tcp://4.3.2.1",
					}
				})

			generateTests("the OVN URL is a hostname",
				"OVN DB host \"foobar.org:4444\" must be an IP address, not a DNS name",
				func() []string {
					return []string{
						"address=tcp://foobar.org:4444",
					}
				})

			generateTests("certs are provided for the TCP scheme",
				"certificate or key given; perhaps you mean to use the 'ssl' scheme?",
				func() []string {
					return []string{
						"address=tcp://1.2.3.4:444",
						"client-privkey=/bar/baz/foo",
					}
				})
		})

		Context("does not return an error when", func() {
			generateTests("the SSL scheme is missing a client CA cert", "",
				func() []string {
					return []string{
						"address=ssl://1.2.3.4:444",
						"client-privkey=" + keyFile,
						"client-cert=" + certFile,
						"client-cacert=/foo/bar/baz",
					}
				})

			generateTests("the SSL scheme is missing a private key file", "",
				func() []string {
					return []string{
						"address=ssl://1.2.3.4:444",
						"client-privkey=/foo/bar/baz",
						"client-cert=" + certFile,
						"client-cacert=" + caFile,
					}
				})

			generateTests("the SSL scheme is missing a client cert file", "",
				func() []string {
					return []string{
						"address=ssl://1.2.3.4:444",
						"client-privkey=" + keyFile,
						"client-cert=/foo/bar/baz",
						"client-cacert=" + caFile,
					}
				})

		})
	})
})
