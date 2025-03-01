package commands

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/rewardenv/reward/internal/core"

	"github.com/mitchellh/go-homedir"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

const defaultConfig = `---
log_level: info
debug: false

# Uncomment the following settings to disable default services.
#   These services are enabled by default.
#reward_portainer: 0
#reward_dnsmasq: 0
#reward_tunnel: 0
#reward_mailhog: 0
#reward_phpmyadmin: 0
#reward_elastichq: 0

# If you want to enable the following services, uncomment any of them.
#   These services are disabled by default.
#reward_adminer: 1

# By default Reward makes it possible to resolve the environment's domain to the nginx container's IP address
#   inside the docker network. To disable this behaviour you can uncomment the following line.
#reward_resolve_domain_to_traefik: 0

# By default Reward is not allowed to run commands as root.
#   To disable this check you can uncomment the following line.
#reward_allow_superuser: 1

# By default Reward is going to use sync session for Windows. With WSL2 it's possible to use well performing
#   direct mount from WSL2's drive. It is disabled by default.
#   To enable this functionality, uncomment the following line.
#reward_wsl2_direct_mount: 1

# By default Reward uses CentOS 7 based images. You can experiment with Debian based images with uncommenting this.
#reward_docker_image_base: debian

# By default Reward uses separated nginx + php-fpm containers.Enabling this setting will merge
# them to one "web" container
#reward_single_web_container: 1
`

// InstallCmd represents the install command.
func InstallCmd() error {
	if getReinstallFlag() || getUninstallFlag() {
		return uninstall()
	}

	if !getUninstallFlag() {
		return install()
	}

	return nil
}

// uninstall removes the application's settings.
func uninstall() error {
	appHomeDir := core.GetAppHomeDir()

	// Make sure we are not deleting "/"
	match, _ := regexp.MatchString("^/$", appHomeDir)
	if !match {
		if _, err := os.Stat(appHomeDir); !os.IsNotExist(err) {
			if confirmation := core.AskForConfirmation(
				fmt.Sprintf("Are you sure you want to delete %v?", appHomeDir),
			); confirmation {
				log.Debugf("Deleting: %v\n", appHomeDir)

				err = os.RemoveAll(appHomeDir)

				if err != nil {
					return err
				}
			}

			if confirmation := core.AskForConfirmation(
				fmt.Sprintf("Are you sure you want to delete %v?", viper.GetString(core.AppName+"_config_file")),
			); confirmation {
				log.Debugf("Deleting: %v\n", viper.GetString(core.AppName+"_config_file"))

				err = os.Remove(viper.GetString(core.AppName + "_config_file"))

				if err != nil {
					return err
				}
			}
		} else {
			return fmt.Errorf(core.AppName + " is not installed")
		}
	}

	return nil
}

// install configures the application default settings.
func install() error {
	// On windows this command should run in elevated command prompt
	osDistro := core.GetOSDistro()
	if osDistro == "windows" {
		if !core.IsAdmin() {
			log.Printf("Running %v in an Elevated command prompt...", core.AppName)
			core.RunMeElevated()
		}
	}

	appHomeDir := core.GetAppHomeDir()

	// If we are not directly call installation for cacert, dns, ssh then check if the install marker already exists.
	if !getInstallCaCertFlag() && !getInstallDNSFlag() && !getInstallSSHKeyFlag() && !getInstallSSHConfigFlag() {
		if core.CheckFileExists(getInstallMarkerFilePath()) {
			if !core.AskForConfirmation(strings.Title(core.AppName) + " is already installed. Would you like to reinstall?") {
				return nil
			}
		}
	}

	// Create application's config directory
	if err := core.CreateDir(appHomeDir, getInstallModeFlag()); err != nil {
		return err
	}

	log.Debugf("Chmod %v dir: %v to %v\n", core.AppName, appHomeDir, os.FileMode(getInstallModeFlag()))

	// Change mode for it
	if err := os.Chmod(appHomeDir, os.FileMode(getInstallModeFlag())); err != nil {
		return err
	}

	// If we are not directly call installation for cacert, dns, ssh then create the app's default config file.
	if !getInstallCaCertFlag() && !getInstallDNSFlag() && !getInstallSSHKeyFlag() && !getInstallSSHConfigFlag() {
		configFile := viper.GetString(core.AppName + "_config_file")
		log.Debugln("Creating default config:", configFile)

		if !core.CheckFileExistsAndRecreate(configFile) {
			if err := core.CreateDirAndWriteBytesToFile([]byte(defaultConfig), configFile); err != nil {
				return err
			}
		}
	}

	// Install CA Certificate
	if !getInstallDNSFlag() && !getInstallSSHKeyFlag() && !getInstallSSHConfigFlag() {
		sslDir := filepath.Join(appHomeDir, "ssl")
		caDir := filepath.Join(sslDir, core.CaBaseDir)

		log.Debugln("Creating certificate to directory:", caDir)

		caCertExist := core.CheckCaCertificateExistInDir(caDir)

		if !caCertExist {
			if err := core.CreateCaCertificate(caDir); err != nil {
				return err
			}

			if err := core.InstallCaCertificate(caDir); err != nil {
				return err
			}
		}
	}

	// Install DNS resolver
	if !getInstallCaCertFlag() && !getInstallSSHKeyFlag() && !getInstallSSHConfigFlag() {
		core.InstallDNSResolver()
	}

	// Install common SSH Key for Tunnel
	if !getInstallCaCertFlag() && !getInstallDNSFlag() && !getInstallSSHConfigFlag() {
		keyPath := filepath.Join(appHomeDir, "tunnel", "ssh_key")

		// On linux, if we want to reinstall the pubfile we have to revert its permissions first
		if runtime.GOOS == "linux" && core.CheckFileExists(keyPath) {
			cmdChown := fmt.Sprintf(
				"sudo chown -v %v:%v %v", os.Getuid(), 0, filepath.Join(appHomeDir, "tunnel", "ssh_key.pub"),
			)
			cmd := exec.Command("/bin/sh", "-c", cmdChown)

			log.Debugf("Running command: %v", cmd)

			out, err := cmd.CombinedOutput()

			log.Debugf("output: %v", string(out))

			if err != nil {
				log.Fatalln(err)
			}
		}

		keyFileExist := core.CheckFileExistsAndRecreate(keyPath)
		if !keyFileExist {
			if err := core.GenerateAndSaveSSHKeys(2048, keyPath); err != nil {
				return err
			}
		}

		// Since bind mounts are native on linux to use .pub file as authorized_keys file in tunnel it
		//   must have proper perms.
		if runtime.GOOS == "linux" {
			cmdChown := fmt.Sprintf("sudo chown -v %v:%v %v", 0, 0, filepath.Join(appHomeDir, "tunnel", "ssh_key.pub"))
			cmd := exec.Command("/bin/sh", "-c", cmdChown)

			log.Debugf("Running command: %v", cmd)

			out, err := cmd.CombinedOutput()

			log.Debugf("output: %v", string(out))

			if err != nil {
				log.Fatalln(err)
			}
		}
	}

	if osDistro != "windows" {
		if !getInstallCaCertFlag() && !getInstallDNSFlag() && !getInstallSSHKeyFlag() {
			// Install SSH settings for the SSH tunnel
			if err := core.InstallSSHConfig(); err != nil {
				return err
			}
		}

		// Create composer directory
		home, err := homedir.Dir()
		if err != nil {
			return err
		}

		err = core.CreateDir(home + "/.composer")
		if err != nil {
			return err
		}
	} else {
		_ = core.AskForConfirmation("Press ENTER to continue...", false)
	}

	// Create file which marks the application as already installed (~/.reward/.installed)
	if err := putInstallMarkerFile(); err != nil {
		return err
	}

	// If the install command is not called with --ignore-svcs or the specific install options directly, then
	//    run `reward svc up`.
	if getInstallInitServicesFlag() && !getInstallCaCertFlag() &&
		!getInstallDNSFlag() && !getInstallSSHKeyFlag() && !getInstallSSHConfigFlag() {
		if err := core.CheckDocker(); err != nil {
			return err
		}

		if err := SvcCmd([]string{"up"}); err != nil {
			return err
		}
	}

	return nil
}

// getReinstallFlag returns true if the --reinstall flag is set during the execution.
func getReinstallFlag() bool {
	return viper.GetBool(core.AppName + "_install_reinstall")
}

// getUninstallFlag returns true if the --uninstall flag is set during the execution.
func getUninstallFlag() bool {
	return viper.GetBool(core.AppName + "_install_uninstall")
}

// getInstallCaCertFlag returns true if --install-ca-cert flag is set during the execution.
func getInstallCaCertFlag() bool {
	return viper.GetBool(core.AppName + "_install_ca_cert")
}

// getInstallDNSFlag returns true if --install-dns flag is set during the execution.
func getInstallDNSFlag() bool {
	return viper.GetBool(core.AppName + "_install_dns")
}

// getInstallSSHKeyFlag returns true if --install-ssh-key flag is set during the execution.
func getInstallSSHKeyFlag() bool {
	return viper.GetBool(core.AppName + "_install_ssh_key")
}

// getInstallSSHConfigFlag returns true if --install-ssh-config flag is set during the execution.
func getInstallSSHConfigFlag() bool {
	return viper.GetBool(core.AppName + "_install_ssh_config")
}

// getInstallModeFlag returns an int which represents the app home directory permissions.
func getInstallModeFlag() int {
	return viper.GetInt(core.AppName + "_install_app_home_mode")
}

// getInstallInitServicesFlag returns true if the common services should started during the installation.
func getInstallInitServicesFlag() bool {
	return !viper.GetBool(core.AppName + "_install_ignore_init_svcs")
}

// getInstallMarkerFilePath returns the filepath of the Install Marker file.
func getInstallMarkerFilePath() string {
	return filepath.Join(core.GetAppHomeDir(), ".installed")
}

// putInstallMarkerFile writes an Install Marker file after the application is installed.
func putInstallMarkerFile() error {
	markerFile := getInstallMarkerFilePath()
	timeNow := time.Now().String()

	if err := core.CreateDirAndWriteBytesToFile([]byte(timeNow+"\n"), markerFile); err != nil {
		return err
	}

	return nil
}

// CheckIfInstalled returns an error if the application is not yet installed.
func CheckIfInstalled() error {
	if !core.CheckFileExists(getInstallMarkerFilePath()) {
		if err := InstallCmd(); err != nil {
			return err
		}
	}

	return nil
}
