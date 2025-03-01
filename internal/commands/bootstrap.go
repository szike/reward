package commands

import (
	"bytes"
	"container/list"
	"fmt"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/rewardenv/reward/internal/core"

	"github.com/hashicorp/go-version"
	"github.com/sethvargo/go-password/password"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

// BootstrapCmd represents the bootstrap command.
func BootstrapCmd() error {
	switch core.GetEnvType() {
	case "magento2":
		if err := bootstrapMagento2(); err != nil {
			return err
		}
	case "magento1":
		if err := bootstrapMagento1(); err != nil {
			return err
		}
	case "wordpress":
		if err := bootstrapWordpress(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("currently not supported for bootstrapping")
	}

	return nil
}

// bootstrapMagento2 runs a full Magento 2 bootstrap process.
func bootstrapMagento2() error {
	magentoVersion, err := core.GetMagentoVersion()
	if err != nil {
		return err
	}
	log.Debugln("Magento Version:", magentoVersion.String())

	if !core.AskForConfirmation("Would you like to bootstrap Magento v" + magentoVersion.String() + "?") {
		return nil
	}

	if err := SvcCmd([]string{"up"}); err != nil {
		return err
	}

	if err := SignCertificateCmd([]string{core.GetTraefikDomain()}, true); err != nil {
		return err
	}

	if isNoPull() {
		if err := EnvCmd([]string{"build"}); err != nil {
			return err
		}
	} else {
		if err := EnvCmd([]string{"pull"}); err != nil {
			return err
		}
		if err := EnvCmd([]string{"build"}); err != nil {
			return err
		}
	}

	if err := EnvCmd([]string{"up"}); err != nil {
		return err
	}

	var baseCommand, composeCommand []string
	baseCommand = []string{"exec", "-T", "php-fpm", "bash", "-c"}
	freshInstall := false

	composerCommand := "composer"
	composerVersion := 1
	composerVersionInEnv, err := core.GetComposerVersion()
	if err != nil {
		return err
	}

	// If Magento version is >= 2.4.2 or Composer Version >= 2.0.0 (defined in .env),
	// than strictly use Composer 2 install mechanism.
	//
	// Bug: in go-version the comparison '2.4.2-p1' >= '2.4.2' is false.
	// (go-version thinks it is a pre-release), so the comparison should be > 2.4.1.

	// Constants for comparison.
	minimumMagentoVersionForComposer2, _ := version.NewVersion("2.4.1")
	versionTwo, _ := version.NewVersion("2.0.0")

	if magentoVersion.GreaterThan(minimumMagentoVersionForComposer2) ||
		composerVersionInEnv.GreaterThanOrEqual(versionTwo) {
		composerVersion = 2
	}

	if composerVersion == 2 {
		log.Debugln("Setting default Composer version to 2.x")
		// Change default Composer Version
		composerVersionChangeCommand := append(
			baseCommand,
			`sudo alternatives --set composer /usr/bin/composer2`,
		)

		if err := EnvCmd(composerVersionChangeCommand); err != nil {
			return err
		}
	}

	// Composer Install
	if !isSkipComposerInstall() {
		if isParallel() && composerVersion != 2 {
			if core.IsDebug() {
				composeCommand = append(baseCommand, composerCommand+` global require -vvv --profile hirak/prestissimo`)
			} else {
				composeCommand = append(
					baseCommand, composerCommand+` global require --verbose --profile hirak/prestissimo`,
				)
			}

			if err := EnvCmd(composeCommand); err != nil {
				return err
			}
		}

		if !core.CheckFileExists("composer.json") {
			freshInstall = true

			if core.IsDebug() {
				composeCommand = append(
					baseCommand,
					fmt.Sprintf(
						composerCommand+` create-project `+
							`-vvv --profile --no-install `+
							`--repository-url=https://repo.magento.com/ `+
							`magento/project-%v-edition=%v /tmp/magento-tmp/`,
						getMagentoType(),
						magentoVersion.String(),
					),
				)
			} else {
				composeCommand = append(
					baseCommand,
					fmt.Sprintf(
						composerCommand+` create-project `+
							`--verbose --profile --no-install `+
							`--repository-url=https://repo.magento.com/ `+
							`magento/project-%v-edition=%v /tmp/magento-tmp/`,
						getMagentoType(),
						magentoVersion.String(),
					),
				)
			}

			if err := EnvCmd(composeCommand); err != nil {
				return err
			}

			var moveCommand []string
			if core.IsDebug() {
				moveCommand = append(
					baseCommand, `rsync -vau --remove-source-files `+
						`--chmod=D2775,F644 /tmp/magento-tmp/ /var/www/html/`,
				)
			} else {
				moveCommand = append(
					baseCommand, `rsync -au --remove-source-files `+
						`--chmod=D2775,F644 /tmp/magento-tmp/ /var/www/html/`,
				)
			}

			if err := EnvCmd(moveCommand); err != nil {
				return err
			}
		}

		if core.IsDebug() {
			composeCommand = append(baseCommand, composerCommand+` install -vvv --profile`)
		} else {
			composeCommand = append(baseCommand, composerCommand+` install -v --profile`)
		}

		if err := EnvCmd(composeCommand); err != nil {
			return err
		}

		if isParallel() && composerVersion != 2 {
			if core.IsDebug() {
				composeCommand = append(baseCommand, composerCommand+` global remove hirak/prestissimo -vvv --profile`)
			} else {
				composeCommand = append(
					baseCommand, composerCommand+` global remove hirak/prestissimo --verbose --profile`,
				)
			}

			if err := EnvCmd(composeCommand); err != nil {
				return err
			}
		}
	}

	// Magento Install
	magentoCmdParams := []string{
		"--backend-frontname=" + core.GetMagentoBackendFrontname(),
		"--db-host=db",
		"--db-name=magento",
		"--db-user=magento",
		"--db-password=magento",
	}

	if getDBPrefix() != "" {
		magentoCmdParams = append(
			magentoCmdParams,
			fmt.Sprintf("--db-prefix=%s",
				getDBPrefix()),
		)
	}

	if core.IsServiceEnabled("redis") {
		magentoCmdParams = append(
			magentoCmdParams,
			"--session-save=redis",
			"--session-save-redis-host=redis",
			"--session-save-redis-port=6379",
			"--session-save-redis-db=2",
			"--session-save-redis-max-concurrency=20",
			"--cache-backend=redis",
			"--cache-backend-redis-server=redis",
			"--cache-backend-redis-db=0",
			"--cache-backend-redis-port=6379",
			"--page-cache=redis",
			"--page-cache-redis-server=redis",
			"--page-cache-redis-db=1",
			"--page-cache-redis-port=6379",
		)
	} else {
		magentoCmdParams = append(
			magentoCmdParams,
			"--session-save=files",
		)
	}

	if core.IsServiceEnabled("varnish") {
		magentoCmdParams = append(
			magentoCmdParams,
			"--http-cache-hosts=varnish:80",
		)
	}

	if core.IsServiceEnabled("rabbitmq") {
		magentoCmdParams = append(
			magentoCmdParams,
			"--amqp-host=rabbitmq",
			"--amqp-port=5672",
			"--amqp-user=guest",
			"--amqp-password=guest",
		)

		minVersion, _ := version.NewVersion("2.4.0")
		if magentoVersion.GreaterThan(minVersion) {
			magentoCmdParams = append(
				magentoCmdParams,
				"--consumers-wait-for-messages=0",
			)
		}
	}

	minimumMagentoVersionForElasticsearch, _ := version.NewVersion("2.4.0")
	if core.IsServiceEnabled("elasticsearch") && magentoVersion.GreaterThan(minimumMagentoVersionForElasticsearch) {
		magentoCmdParams = append(
			magentoCmdParams,
			"--search-engine=elasticsearch7",
			"--elasticsearch-host=elasticsearch",
			"--elasticsearch-port=9200",
			"--elasticsearch-index-prefix=magento2",
			"--elasticsearch-enable-auth=0",
			"--elasticsearch-timeout=15",
		)
	}

	// magento install command
	composeCommand = append(baseCommand, `bin/magento setup:install `+strings.Join(magentoCmdParams, " "))
	if err := EnvCmd(composeCommand); err != nil {
		return err
	}

	magentoCmdParams = []string{
		fmt.Sprintf("web/unsecure/base_url http://%v/", core.GetTraefikFullDomain()),
	}
	composeCommand = append(baseCommand, `bin/magento config:set `+strings.Join(magentoCmdParams, " "))

	if err := EnvCmd(composeCommand); err != nil {
		return err
	}

	magentoCmdParams = []string{
		fmt.Sprintf("web/secure/base_url https://%v/", core.GetTraefikFullDomain()),
	}
	composeCommand = append(baseCommand, `bin/magento config:set `+strings.Join(magentoCmdParams, " "))

	if err := EnvCmd(composeCommand); err != nil {
		return err
	}

	magentoCmdParams = []string{
		"--lock-env web/secure/offloader_header X-Forwarded-Proto",
	}
	composeCommand = append(baseCommand, `bin/magento config:set `+strings.Join(magentoCmdParams, " "))

	if err := EnvCmd(composeCommand); err != nil {
		return err
	}

	magentoCmdParams = []string{
		"web/secure/use_in_frontend 1",
	}
	composeCommand = append(baseCommand, `bin/magento config:set `+strings.Join(magentoCmdParams, " "))

	if err := EnvCmd(composeCommand); err != nil {
		return err
	}

	magentoCmdParams = []string{
		"web/secure/use_in_adminhtml 1",
	}
	composeCommand = append(baseCommand, `bin/magento config:set `+strings.Join(magentoCmdParams, " "))

	if err := EnvCmd(composeCommand); err != nil {
		return err
	}

	magentoCmdParams = []string{
		"web/seo/use_rewrites 1",
	}
	composeCommand = append(baseCommand, `bin/magento config:set `+strings.Join(magentoCmdParams, " "))

	if err := EnvCmd(composeCommand); err != nil {
		return err
	}

	if core.IsServiceEnabled("varnish") {
		magentoCmdParams = []string{
			"--lock-env system/full_page_cache/caching_application 2",
		}
		composeCommand = append(baseCommand, `bin/magento config:set `+strings.Join(magentoCmdParams, " "))

		if err := EnvCmd(composeCommand); err != nil {
			return err
		}

		magentoCmdParams = []string{
			"--lock-env system/full_page_cache/ttl 604800",
		}
		composeCommand = append(baseCommand, `bin/magento config:set `+strings.Join(magentoCmdParams, " "))

		if err := EnvCmd(composeCommand); err != nil {
			return err
		}
	}

	magentoCmdParams = []string{
		"--lock-env catalog/search/enable_eav_indexer 1",
	}
	magentoCommand := append(baseCommand, `bin/magento config:set `+strings.Join(magentoCmdParams, " "))

	if err := EnvCmd(magentoCommand); err != nil {
		return err
	}

	if core.IsServiceEnabled("elasticsearch") && magentoVersion.GreaterThan(minimumMagentoVersionForElasticsearch) {
		magentoCmdParams = []string{
			"--lock-env catalog/search/engine elasticsearch7",
		}
		magentoCommand = append(baseCommand, `bin/magento config:set `+strings.Join(magentoCmdParams, " "))

		if err := EnvCmd(magentoCommand); err != nil {
			return err
		}

		magentoCmdParams = []string{
			"--lock-env catalog/search/elasticsearch7_server_hostname elasticsearch",
		}
		magentoCommand = append(baseCommand, `bin/magento config:set `+strings.Join(magentoCmdParams, " "))

		if err := EnvCmd(magentoCommand); err != nil {
			return err
		}

		magentoCmdParams = []string{
			"--lock-env catalog/search/elasticsearch7_server_port 9200",
		}
		magentoCommand = append(baseCommand, `bin/magento config:set `+strings.Join(magentoCmdParams, " "))

		if err := EnvCmd(magentoCommand); err != nil {
			return err
		}

		magentoCmdParams = []string{
			"--lock-env catalog/search/elasticsearch7_index_prefix magento2",
		}
		magentoCommand = append(baseCommand, `bin/magento config:set `+strings.Join(magentoCmdParams, " "))

		if err := EnvCmd(magentoCommand); err != nil {
			return err
		}

		magentoCmdParams = []string{
			"--lock-env catalog/search/elasticsearch7_enable_auth 0",
		}
		magentoCommand = append(baseCommand, `bin/magento config:set `+strings.Join(magentoCmdParams, " "))

		if err := EnvCmd(magentoCommand); err != nil {
			return err
		}

		magentoCmdParams = []string{
			"--lock-env catalog/search/elasticsearch7_server_timeout 15",
		}
		magentoCommand = append(baseCommand, `bin/magento config:set `+strings.Join(magentoCmdParams, " "))

		if err := EnvCmd(magentoCommand); err != nil {
			return err
		}
	}

	magentoCommand = append(baseCommand, `bin/magento deploy:mode:set -s `+getMagentoMode())
	if err := EnvCmd(magentoCommand); err != nil {
		return err
	}

	// Disable TFA for local development.
	minimumMagentoVersionForMFA, _ := version.NewVersion("2.4.0")
	if magentoVersion.GreaterThan(minimumMagentoVersionForMFA) && isMagentoDisableTFA() {
		magentoCommand = append(baseCommand, `bin/magento module:disable Magento_TwoFactorAuth`)
		if err := EnvCmd(magentoCommand); err != nil {
			return err
		}
	}

	adminPassword, err := password.Generate(16, 2, 0, false, false)
	if err != nil {
		return err
	}

	magentoCmdParams = []string{
		"--admin-password=" + adminPassword,
		"--admin-user=localadmin",
		"--admin-firstname=Local",
		"--admin-lastname=Admin",
		`--admin-email="admin@example.com"`,
	}
	magentoCommand = append(baseCommand, `bin/magento admin:user:create `+strings.Join(magentoCmdParams, " "))

	if err = EnvCmd(magentoCommand); err != nil {
		return err
	}

	// sample data
	if freshInstall && (isWithSampleData() || isFullBootstrap()) {
		shellCommand := append(
			baseCommand,
			`mkdir -p /var/www/html/var/composer_home/ \
			&& cp -va ~/.composer/auth.json /var/www/html/var/composer_home/auth.json`,
		)
		if err := EnvCmd(shellCommand); err != nil {
			return err
		}

		if core.IsDebug() {
			magentoCommand = append(baseCommand, `php bin/magento -vvv sampledata:deploy`)
		} else {
			magentoCommand = append(baseCommand, `php bin/magento -v sampledata:deploy`)
		}

		if err := EnvCmd(magentoCommand); err != nil {
			return err
		}

		if core.IsDebug() {
			magentoCommand = append(baseCommand, `bin/magento setup:upgrade -vvv`)
		} else {
			magentoCommand = append(baseCommand, `bin/magento setup:upgrade -v`)
		}

		if err := EnvCmd(magentoCommand); err != nil {
			return err
		}
	}

	if isFullBootstrap() {
		magentoCommand = append(baseCommand, `bin/magento indexer:reindex`)

		if err := EnvCmd(magentoCommand); err != nil {
			return err
		}
	}

	if resetAdminURL() {
		magentoCommand = append(baseCommand, `bin/magento config:set admin/url/use_custom 0`)

		if err := EnvCmd(magentoCommand); err != nil {
			return err
		}

		magentoCommand = append(baseCommand, `bin/magento config:set admin/url/use_custom_path 0`)

		if err := EnvCmd(magentoCommand); err != nil {
			return err
		}

	}

	magentoCommand = append(baseCommand, `bin/magento cache:flush`)

	if err := EnvCmd(magentoCommand); err != nil {
		return err
	}

	log.Println("Base Url: https://" + core.GetTraefikFullDomain())
	log.Println("Backend Url: https://" + core.GetTraefikFullDomain() + "/" + core.GetMagentoBackendFrontname())
	log.Println("Admin user: localadmin")
	log.Println("Admin password: " + adminPassword)
	log.Println("Installation finished successfully.")

	return nil
}

// bootstrapMagento1 runs a full Magento 1 bootstrap process.
// Note: it will not install Magento 1 from zero, but only configures Magento 1's local.xml.
func bootstrapMagento1() error {
	magentoVersion, err := core.GetMagentoVersion()
	if err != nil {
		return err
	}
	log.Debugln("Magento Version:", magentoVersion.String())

	if !core.AskForConfirmation("Would you like to bootstrap Magento v" + magentoVersion.String() + "?") {
		return nil
	}

	if err := SvcCmd([]string{"up"}); err != nil {
		return err
	}

	if err := SignCertificateCmd([]string{core.GetTraefikDomain()}, true); err != nil {
		return err
	}

	if isNoPull() {
		if err := EnvCmd([]string{"build"}); err != nil {
			return err
		}
	} else {
		if err := EnvCmd([]string{"pull"}); err != nil {
			return err
		}
		if err := EnvCmd([]string{"build"}); err != nil {
			return err
		}
	}

	if err := EnvCmd([]string{"up"}); err != nil {
		return err
	}

	var baseCommand, composeCommand []string
	baseCommand = []string{"exec", "-T", "php-fpm", "bash", "-c"}

	// Composer Install
	if core.CheckFileExists("composer.json") {
		if isParallel() {
			if core.IsDebug() {
				composeCommand = append(baseCommand, `composer global require -vvv --profile hirak/prestissimo`)
			} else {
				composeCommand = append(baseCommand, `composer global require --verbose --profile hirak/prestissimo`)
			}

			if err := EnvCmd(composeCommand); err != nil {
				return err
			}
		}

		if core.IsDebug() {
			composeCommand = append(baseCommand, `composer install -vvv --profile`)
		} else {
			composeCommand = append(baseCommand, `composer install -v --profile`)
		}

		if err := EnvCmd(composeCommand); err != nil {
			return err
		}

		if isParallel() {
			if core.IsDebug() {
				composeCommand = append(baseCommand, `composer global remove hirak/prestissimo -vvv --profile`)
			} else {
				composeCommand = append(baseCommand, `composer global remove hirak/prestissimo --verbose --profile`)
			}

			if err := EnvCmd(composeCommand); err != nil {
				return err
			}
		}
	}

	localXMLFilePath := filepath.Join(core.GetCwd(), "app", "etc", "local.xml")
	if core.CheckFileExistsAndRecreate(localXMLFilePath) {
		return nil
	}

	var bs bytes.Buffer

	localXMLTemplate := new(template.Template)
	tmpList := new(list.List)

	localXMLTemplatePath := []string{
		filepath.Join("templates", "_magento1", "local.xml"),
	}

	log.Traceln("template paths:")
	log.Traceln(localXMLTemplatePath)

	err = core.AppendTemplatesFromPathsStatic(localXMLTemplate, tmpList, localXMLTemplatePath)
	if err != nil {
		return err
	}

	for e := tmpList.Front(); e != nil; e = e.Next() {
		tplName := fmt.Sprint(e.Value)

		err = core.ExecuteTemplate(localXMLTemplate.Lookup(tplName), &bs)
		if err != nil {
			return err
		}

		err = core.CreateDirAndWriteBytesToFile(bs.Bytes(), localXMLFilePath)
		if err != nil {
			return err
		}
	}

	magerunCmdParams := []string{
		fmt.Sprintf("web/unsecure/base_url http://%v/", core.GetTraefikFullDomain()),
	}
	magerunCommand := append(baseCommand, `/usr/bin/n98-magerun config:set `+strings.Join(magerunCmdParams, " "))

	if err := EnvCmd(magerunCommand); err != nil {
		return err
	}

	magerunCmdParams = []string{
		fmt.Sprintf("web/secure/base_url https://%v/", core.GetTraefikFullDomain()),
	}
	magerunCommand = append(baseCommand, `/usr/bin/n98-magerun config:set `+strings.Join(magerunCmdParams, " "))

	if err := EnvCmd(magerunCommand); err != nil {
		return err
	}

	magerunCmdParams = []string{
		"web/secure/use_in_frontend 1",
	}
	magerunCommand = append(baseCommand, `/usr/bin/n98-magerun config:set `+strings.Join(magerunCmdParams, " "))

	if err := EnvCmd(magerunCommand); err != nil {
		return err
	}

	magerunCmdParams = []string{
		"web/secure/use_in_adminhtml 1",
	}
	magerunCommand = append(baseCommand, `/usr/bin/n98-magerun config:set `+strings.Join(magerunCmdParams, " "))

	if err := EnvCmd(magerunCommand); err != nil {
		return err
	}

	adminPassword, err := password.Generate(16, 2, 0, false, false)
	if err != nil {
		return err
	}

	magentoCmdParams := []string{
		"localadmin",        // username
		`admin@example.com`, // email
		adminPassword,       // password
		"Local",             // firstname
		"Admin",             // lastname
	}
	magerunCommand = append(baseCommand, `/usr/bin/n98-magerun admin:user:create `+strings.Join(magentoCmdParams, " "))

	if err = EnvCmd(magerunCommand); err != nil {
		return err
	}

	magerunCommand = append(baseCommand, `/usr/bin/n98-magerun cache:flush`)

	if err := EnvCmd(magerunCommand); err != nil {
		return err
	}

	log.Println("Base Url: https://" + core.GetTraefikFullDomain())
	log.Println("Backend Url: https://" + core.GetTraefikFullDomain() + "/" + core.GetMagentoBackendFrontname())
	log.Println("Admin user: localadmin")
	log.Println("Admin password: " + adminPassword)
	log.Println("Installation finished successfully.")

	log.Println("Installation finished successfully.")

	return nil
}

// bootstrapWordpress runs a full WordPress bootstrap process.
func bootstrapWordpress() error {
	if !core.AskForConfirmation("Would you like to bootstrap Wordpress?") {
		return nil
	}

	if err := SvcCmd([]string{"up"}); err != nil {
		return err
	}

	if err := SignCertificateCmd([]string{core.GetTraefikDomain()}, true); err != nil {
		return err
	}

	if isNoPull() {
		if err := EnvCmd([]string{"build"}); err != nil {
			return err
		}
	} else {
		if err := EnvCmd([]string{"pull"}); err != nil {
			return err
		}
		if err := EnvCmd([]string{"build"}); err != nil {
			return err
		}
	}

	if err := EnvCmd([]string{"up"}); err != nil {
		return err
	}

	var baseCommand, bashCommand []string
	baseCommand = []string{"exec", "-T", "php-fpm", "bash", "-c"}

	// Install
	if !core.CheckFileExists("index.php") {
		log.Println("Downloading and installing wordpress...")

		bashCommand = append(baseCommand, `wget -qO /tmp/wordpress.tar.gz https://wordpress.org/latest.tar.gz`)

		if err := EnvCmd(bashCommand); err != nil {
			return err
		}

		bashCommand = append(baseCommand, `tar -zxf /tmp/wordpress.tar.gz --strip-components=1 -C /var/www/html`)

		if err := EnvCmd(bashCommand); err != nil {
			return err
		}

		bashCommand = append(baseCommand, `rm -f /tmp/wordpress.tar.gz`)

		if err := EnvCmd(bashCommand); err != nil {
			return err
		}
	}

	wpConfigFilePath := filepath.Join(core.GetCwd(), "wp-config.php")
	if core.CheckFileExistsAndRecreate(wpConfigFilePath) {
		return nil
	}

	var bs bytes.Buffer

	wpConfigTemplate := new(template.Template)
	wptmpList := new(list.List)

	wpConfigTemplatePath := []string{
		filepath.Join("templates", "_wordpress", "wp-config.php"),
	}

	log.Traceln("template paths:")
	log.Traceln(wpConfigTemplatePath)

	err := core.AppendTemplatesFromPathsStatic(wpConfigTemplate, wptmpList, wpConfigTemplatePath)
	if err != nil {
		return err
	}

	if getDBPrefix() != "" {
		viper.Set("wordpress_table_prefix", getDBPrefix())
	}

	for e := wptmpList.Front(); e != nil; e = e.Next() {
		tplName := fmt.Sprint(e.Value)

		err = core.ExecuteTemplate(wpConfigTemplate.Lookup(tplName), &bs)
		if err != nil {
			return err
		}

		err = core.CreateDirAndWriteBytesToFile(bs.Bytes(), wpConfigFilePath)
		if err != nil {
			return err
		}
	}

	log.Println("Base Url: https://" + core.GetTraefikFullDomain())
	log.Println("Installation finished successfully.")

	return nil
}

// isFullBootstrap checks if full bootstrap is enabled in Viper settings.
func isFullBootstrap() bool {
	if viper.IsSet(core.AppName + "_full_bootstrap") {
		return viper.GetBool(core.AppName + "_full_bootstrap")
	}

	return false
}

// isParallel checks if composer parallel mode is enabled in Viper settings.
func isParallel() bool {
	if viper.IsSet(core.AppName + "_composer_no_parallel") {
		return !viper.GetBool(core.AppName + "_composer_no_parallel")
	}

	return true
}

// isSkipComposerInstall checks if composer install is disabled in Viper settings.
func isSkipComposerInstall() bool {
	if viper.IsSet(core.AppName + "_skip_composer_install") {
		return viper.GetBool(core.AppName + "_skip_composer_install")
	}

	return false
}

// isNoPull checks if docker-compose pull is disabled in Viper settings.
func isNoPull() bool {
	if viper.IsSet(core.AppName + "_no_pull") {
		return viper.GetBool(core.AppName + "_no_pull")
	}

	return false
}

// isWithSampleData checks if Magento 2 sample data is enabled in Viper settings.
func isWithSampleData() bool {
	if viper.IsSet(core.AppName + "_with_sampledata") {
		return viper.GetBool(core.AppName + "_with_sampledata")
	}

	return false
}

// isMagentoDisableTFA checks if the installer should Disable TwoFactorAuth module in Viper settings.
func isMagentoDisableTFA() bool {
	if viper.IsSet(core.AppName + "_magento_disable_tfa") {
		return viper.GetBool(core.AppName + "_magento_disable_tfa")
	}

	return false
}

// resetAdminURL checks if the installer should Reset the Admin URLs in Viper settings.
func resetAdminURL() bool {
	if viper.IsSet(core.AppName + "_reset_admin_url") {
		return viper.GetBool(core.AppName + "_reset_admin_url")
	}

	return false
}

// GetMagentoType returns Magento type: enterprise or community (default: community).
func getMagentoType() string {
	if viper.IsSet(core.AppName + "_magento_type") {
		if viper.GetString(core.AppName+"_magento_type") == "enterprise" ||
			viper.GetString(core.AppName+"_magento_type") == "commerce" {
			return "enterprise"
		}
	}

	return "community"
}

// getMagentoMode returns Magento mode: developer or production (default: developer).
func getMagentoMode() string {
	if viper.IsSet(core.AppName + "_magento_mode") {
		if viper.GetString(core.AppName+"_magento_mode") == "production" {
			return "production"
		}
	}

	return "developer"
}

// getDBPrefix returns db-prefix
func getDBPrefix() string {
	if viper.IsSet(core.AppName + "_db_prefix") {
		return viper.GetString(core.AppName + "_db_prefix")
	}
	return ""
}
