package acceptance_tests_test

import (
	"encoding/json"
	"encoding/pem"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/ginkgo/extensions/table"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
	"github.com/pavel-v-chernykh/keystore-go"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"fmt"
	"gopkg.in/yaml.v2"
)

type row struct {
	Stdout   string `json:"stdout"`
	ExitCode string `json:"exit_code"`
}

type table struct {
	Rows []row
}

type sshResult struct {
	Tables []table
}

var _ = Describe("UaaRelease", func() {

	AfterEach(func() {
		deleteUAA()
	})

	DescribeTable("uaa truststore", func(addedOSConfCertificates int, optFiles ...string) {
		numCertificatesBeforeDeploy := getNumOfOSCertificates()
		deployUAA(optFiles...)
		numCertificatesAfterDeploy := getNumOfOSCertificates()
		Expect(numCertificatesAfterDeploy).To(Equal(numCertificatesBeforeDeploy + addedOSConfCertificates))

		caCertificatesPemEncodedMap := buildCACertificatesPemEncodedMap()

		var trustStoreMap map[string]interface{}
		Eventually(func() map[string]interface{} {
			trustStoreMap = buildTruststoreMap()
			return trustStoreMap
		}, 5*time.Minute, 10*time.Second).Should(HaveLen(len(caCertificatesPemEncodedMap)))

		for key := range caCertificatesPemEncodedMap {
			Expect(trustStoreMap).To(HaveKey(key))
		}

	},
		Entry("without BPM enabled and os-conf not adding certs", 0, "./opsfiles/disable-bpm.yml", "./opsfiles/os-conf-0-certificate.yml"),
		Entry("without BPM enabled and with os-conf + ca_cert property adding certificates", 11, "./opsfiles/disable-bpm.yml", "./opsfiles/os-conf-1-certificate.yml", "./opsfiles/load-more-ca-certs.yml"),

		Entry("with BPM enabled and os-conf not adding certs", 0, "./opsfiles/enable-bpm.yml", "./opsfiles/os-conf-0-certificate.yml"),
		Entry("with BPM enabled and and with os-conf + ca_cert property adding certificates", 11, "./opsfiles/enable-bpm.yml", "./opsfiles/os-conf-1-certificate.yml", "./opsfiles/load-more-ca-certs.yml"),
	)

	Context("consuming the `database` link", func() {
		It("should refer to the database host by a BOSH DNS URL", func() {
			// TODO: modify deployment to add some job that produces a `database` link
			deployUAA()

			// fetch UAA config manifest from VM
			fetchUaaConfigCmd := exec.Command(boshBinaryPath, "scp", "uaa:/var/vcap/jobs/uaa/config/uaa.yml", os.TempDir() + "/uaa.yml")
			session, err := gexec.Start(fetchUaaConfigCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			Eventually(session, 5*time.Minute).Should(gexec.Exit(0))

			// inspect contents of config file
			uaaConfigFile, err := os.Open(os.TempDir() + "/uaa.yml")
			Expect(err).NotTo(HaveOccurred())

			bs, err := ioutil.ReadAll(uaaConfigFile)
			Expect(err).NotTo(HaveOccurred())

			uaaConfigMap := make(map[string]interface{})
			err = yaml.Unmarshal(bs, nil)
			Expect(err).NotTo(HaveOccurred())

			// validate properties in config manifest
			databaseBlock, ok := uaaConfigMap["database"].(map[string]interface{})
			Expect(ok).To(BeTrue())
			Expect(databaseBlock).NotTo(BeNil())
			Expect(databaseBlock).To(HaveKeyWithValue("url", "jdbc:mysql://mysql.bosh-dns.com:3306/uaa"))
		})
	})
})

var _ = Describe("uaa-rotator-errand", func() {
	It("running the key-rotator errand should exit 0", func() {
		deployUAA()

		runErrandCmd := exec.Command(boshBinaryPath, "run-errand", "uaa_key_rotator")
		session, err := gexec.Start(runErrandCmd, GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())
		Eventually(session, 5*time.Minute).Should(gexec.Exit(0))
	})
})

var portTests = func(bpmOpsFile string) {
	Context(fmt.Sprintf("bpm %s", bpmOpsFile), func() {
		var opsFiles []string

		BeforeEach(func() {
			opsFiles = []string{bpmOpsFile}
		})

		JustBeforeEach(func() {
			deployUAA(opsFiles...)
		})

		Context(fmt.Sprintf("when deploying a http only uaa %s", bpmOpsFile), func() {
			BeforeEach(func() {
				opsFiles = append(opsFiles, "./opsfiles/enable-http.yml", "./opsfiles/disable-https.yml")
			})

			It(fmt.Sprintf("upgrading to https only should be healthy on the https port %s", bpmOpsFile), func() {
				deployUAA(bpmOpsFile, "./opsfiles/enable-https.yml", "./opsfiles/disable-http.yml")
				assertUAAIsHealthy("/var/vcap/jobs/uaa/bin/health_check")
				assertUAAIsHealthy("/var/vcap/jobs/uaa/bin/dns_health_check")
			})
		})

		Context("with http only on a custom port", func() {
			BeforeEach(func() {
				opsFiles = append(opsFiles, "./opsfiles/non-default-uaa-port.yml")
			})

			It("health_check should check the health on the correct port", func() {
				assertUAAIsHealthy("/var/vcap/jobs/uaa/bin/health_check")
				assertUAAIsHealthy("/var/vcap/jobs/uaa/bin/dns_health_check")
			})
		})

		Context("with https only", func() {
			BeforeEach(func() {
				opsFiles = append(opsFiles, "./opsfiles/enable-ssl.yml")
			})

			It("health_check should check the health on the correct port", func() {
				assertUAAIsHealthy("/var/vcap/jobs/uaa/bin/health_check")
				assertUAAIsHealthy("/var/vcap/jobs/uaa/bin/dns_health_check")
			})
		})
	})
}

var _ = Describe("setting a custom UAA port", func() {
	portTests("./opsfiles/enable-bpm.yml")
	portTests("./opsfiles/disable-bpm.yml")
})

func assertUAAIsHealthy(healthCheckPath string) {
	healthCheckCmd := exec.Command(boshBinaryPath, []string{"--json", "ssh", "--results", "uaa", "-c", healthCheckPath}...)
	session, err := gexec.Start(healthCheckCmd, GinkgoWriter, GinkgoWriter)
	Expect(err).NotTo(HaveOccurred())
	Eventually(session, 5*time.Minute).Should(gexec.Exit(0))
}

func buildTruststoreMap() map[string]interface{} {
	By("downloading the truststore")
	localKeyStorePath := scpTruststore()
	localKeyStoreFile, err := os.Open(localKeyStorePath)
	Expect(err).NotTo(HaveOccurred())
	keyStoreDecoded, err := keystore.Decode(localKeyStoreFile, []byte("changeit"))
	Expect(err).NotTo(HaveOccurred())

	trustStoreCertMap := map[string]interface{}{}
	for _, cert := range keyStoreDecoded {
		if trustedCertEntry, isCorrectType := cert.(*keystore.TrustedCertificateEntry); isCorrectType {
			block := &pem.Block{
				Type:  "CERTIFICATE",
				Bytes: trustedCertEntry.Certificate.Content,
			}
			trustStoreCertMap[string(pem.EncodeToMemory(block))] = nil
		}
	}

	return trustStoreCertMap
}

func buildCACertificatesPemEncodedMap() map[string]interface{} {
	By("downloading the os ssl ca certificates")
	caCertificatesPath := scpOSSSLCertFile()
	caCertificatesContent, err := ioutil.ReadFile(caCertificatesPath)
	Expect(err).NotTo(HaveOccurred())

	var caCertificatesPem *pem.Block
	var rest []byte

	caCertificates := map[string]interface{}{}

	for {
		caCertificatesPem, rest = pem.Decode(caCertificatesContent)

		if caCertificatesPem == nil {
			break
		}
		caCertificates[string(pem.EncodeToMemory(caCertificatesPem))] = nil
		caCertificatesContent = rest
	}

	return caCertificates
}

func scpOSSSLCertFile() string {
	caCertificatesPath := filepath.Join(os.TempDir(), "ca-certificates.crt")
	cmd := exec.Command(boshBinaryPath, "scp", "uaa:/etc/ssl/certs/ca-certificates.crt", caCertificatesPath)
	session, err := gexec.Start(cmd, GinkgoWriter, GinkgoWriter)
	Expect(err).NotTo(HaveOccurred())
	Eventually(session, 10*time.Second).Should(gexec.Exit(0))

	return caCertificatesPath
}

func scpTruststore() string {
	localKeyStorePath := filepath.Join(os.TempDir(), "cacerts")
	cmd := exec.Command(boshBinaryPath, "scp", "uaa:/var/vcap/data/uaa/cert-cache/cacerts", localKeyStorePath)
	session, err := gexec.Start(cmd, GinkgoWriter, GinkgoWriter)
	Expect(err).NotTo(HaveOccurred())
	Eventually(session, 10*time.Second).Should(gexec.Exit(0))
	return localKeyStorePath
}

func getNumOfOSCertificates() int {
	caCertificatesSSHStdoutCmd := exec.Command(boshBinaryPath, []string{"--json", "ssh", "--results", "uaa", "-c", "sudo grep 'END CERTIFICATE' /etc/ssl/certs/ca-certificates.crt | wc -l"}...)
	session, err := gexec.Start(caCertificatesSSHStdoutCmd, GinkgoWriter, GinkgoWriter)
	Expect(err).NotTo(HaveOccurred())
	Eventually(session, 10*time.Second).Should(gexec.Exit(0))

	var result = &sshResult{}
	err = json.Unmarshal(session.Out.Contents(), result)
	Expect(err).NotTo(HaveOccurred())
	Expect(result.Tables).To(HaveLen(1))
	Expect(result.Tables[0].Rows).To(HaveLen(1))

	numOfCerts, err := strconv.Atoi(
		strings.TrimFunc(string(result.Tables[0].Rows[0].Stdout), func(r rune) bool {
			return !unicode.IsNumber(r)
		}),
	)
	Expect(err).NotTo(HaveOccurred())
	Expect(numOfCerts).To(BeNumerically(">=", 148))
	return numOfCerts
}
