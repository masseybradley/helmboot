package run

import (
	"fmt"
	"os"
	"strings"

	"github.com/jenkins-x-labs/helmboot/pkg/common"
	"github.com/jenkins-x-labs/helmboot/pkg/helmer"
	"github.com/jenkins-x-labs/helmboot/pkg/jxadapt"
	"github.com/jenkins-x-labs/helmboot/pkg/reqhelpers"
	"github.com/jenkins-x-labs/helmboot/pkg/secretmgr"
	"github.com/jenkins-x/jx/pkg/cmd/boot"
	"github.com/jenkins-x/jx/pkg/cmd/clients"
	"github.com/jenkins-x/jx/pkg/cmd/helper"
	"github.com/jenkins-x/jx/pkg/cmd/opts"
	"github.com/jenkins-x/jx/pkg/cmd/templates"
	"github.com/jenkins-x/jx/pkg/config"
	"github.com/jenkins-x/jx/pkg/gits"
	"github.com/jenkins-x/jx/pkg/jxfactory"
	"github.com/jenkins-x/jx/pkg/kube"
	"github.com/jenkins-x/jx/pkg/log"
	"github.com/jenkins-x/jx/pkg/util"
	"github.com/jenkins-x/jx/pkg/versionstream"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

// RunOptions contains the command line arguments for this command
type RunOptions struct {
	boot.BootOptions
	JXFactory jxfactory.Factory
	Gitter    gits.Gitter
	ChartName string
	BatchMode bool
	JobMode   bool
}

var (
	stepCustomPipelineLong = templates.LongDesc(`
		This command boots up Jenkins and/or Jenkins X in a Kubernetes cluster using GitOps

`)

	stepCustomPipelineExample = templates.Examples(`
		# runs the boot Job to install for the first time
		%s run --git-url https://github.com/myorg/environment-mycluster-dev.git

		# runs the boot Job to upgrade a cluster from the latest in git
		%s run 
`)
)

const (
	defaultChartName = "jx-labs/jxl-boot"
)

// NewCmdRun creates the new command
func NewCmdRun() *cobra.Command {
	options := RunOptions{}
	command := &cobra.Command{
		Use:     "run",
		Short:   "boots up Jenkins and/or Jenkins X in a Kubernetes cluster using GitOps by triggering a Kubernetes Job inside the cluster",
		Long:    stepCustomPipelineLong,
		Example: fmt.Sprintf(stepCustomPipelineExample, common.BinaryName, common.BinaryName),
		Run: func(command *cobra.Command, args []string) {
			common.SetLoggingLevel(command, args)
			err := options.Run()
			helper.CheckErr(err)
		},
	}
	command.Flags().StringVarP(&options.Dir, "dir", "d", ".", "the directory to look for the Jenkins X Pipeline, requirements and charts")
	command.Flags().StringVarP(&options.GitURL, "git-url", "u", "", "override the Git clone URL for the JX Boot source to start from, ignoring the versions stream. Normally specified with git-ref as well")
	command.Flags().StringVarP(&options.GitRef, "git-ref", "", "master", "override the Git ref for the JX Boot source to start from, ignoring the versions stream. Normally specified with git-url as well")
	command.Flags().StringVarP(&options.ChartName, "chart", "c", defaultChartName, "the chart name to use to install the boot Job")
	command.Flags().StringVarP(&options.VersionStreamURL, "versions-repo", "", common.DefaultVersionsURL, "the bootstrap URL for the versions repo. Once the boot config is cloned, the repo will be then read from the jx-requirements.yml")
	command.Flags().StringVarP(&options.VersionStreamRef, "versions-ref", "", common.DefaultVersionsRef, "the bootstrap ref for the versions repo. Once the boot config is cloned, the repo will be then read from the jx-requirements.yml")
	command.Flags().StringVarP(&options.HelmLogLevel, "helm-log", "v", "", "sets the helm logging level from 0 to 9. Passed into the helm CLI via the '-v' argument. Useful to diagnose helm related issues")
	command.Flags().StringVarP(&options.RequirementsFile, "requirements", "r", "", "requirements file which will overwrite the default requirements file")

	defaultBatchMode := false
	if os.Getenv("JX_BATCH_MODE") == "true" {
		defaultBatchMode = true
	}
	command.PersistentFlags().BoolVarP(&options.BatchMode, "batch-mode", "b", defaultBatchMode, "Runs in batch mode without prompting for user input")

	command.Flags().BoolVarP(&options.JobMode, "job", "", false, "if running inside the cluster lets still default to creating the boot Job rather than running boot locally")

	return command
}

// Run implements the command
func (o *RunOptions) Run() error {
	if o.JobMode || !IsInCluster() {
		return o.RunBootJob()
	}
	bo := &o.BootOptions
	if bo.CommonOptions == nil {
		f := clients.NewFactory()
		bo.CommonOptions = opts.NewCommonOptionsWithTerm(f, os.Stdin, os.Stdout, os.Stderr)
		bo.BatchMode = o.BatchMode
	}
	return bo.Run()
}

// RunBootJob runs the boot installer Job
func (o *RunOptions) RunBootJob() error {
	requirements, gitURL, err := o.findRequirementsAndGitURL()
	if err != nil {
		return err
	}
	if gitURL == "" {
		return util.MissingOption("git-url")
	}

	clusterName := requirements.Cluster.ClusterName
	log.Logger().Infof("running helmboot Job for cluster %s with git URL %s", util.ColorInfo(clusterName), util.ColorInfo(gitURL))

	log.Logger().Debug("deleting the old jx-boot chart ...")
	c := util.Command{
		Name: "helm",
		Args: []string{"delete", "jx-boot"},
	}
	_, err = c.RunWithoutRetry()
	if err != nil {
		log.Logger().Debugf("failed to delete the old jx-boot chart: %s", err.Error())
	}

	err = o.verifyBootSecret(requirements)
	if err != nil {
		return err
	}

	// lets add helm repository for jx-labs
	h := helmer.NewHelmCLI(o.Dir)
	_, err = helmer.AddHelmRepoIfMissing(h, helmer.LabsChartRepository, "jx-labs", "", "")
	if err != nil {
		return errors.Wrap(err, "failed to add Jenkins X Labs chart repository")
	}
	err = h.UpdateRepo()
	if err != nil {
		log.Logger().Warnf("failed to update helm repositories: %s", err.Error())
	}

	version, err := o.findChartVersion(requirements)
	if err != nil {
		return err
	}

	c = reqhelpers.GetBootJobCommand(requirements, gitURL, o.ChartName, version)

	commandLine := fmt.Sprintf("%s %s", c.Name, strings.Join(c.Args, " "))

	log.Logger().Infof("running the command:\n\n%s\n\n", util.ColorInfo(commandLine))

	_, err = c.RunWithoutRetry()
	if err != nil {
		return errors.Wrapf(err, "failed to run command %s", commandLine)
	}

	return o.tailJobLogs()
}

func (o *RunOptions) tailJobLogs() error {
	a := jxadapt.NewJXAdapter(o.JXFactory, o.Git(), o.BatchMode)
	client, ns, err := o.JXFactory.CreateKubeClient()
	if err != nil {
		return err
	}
	co := a.NewCommonOptions()

	selector := map[string]string{
		"job-name": "jx-boot",
	}
	containerName := "boot"
	podInterface := client.CoreV1().Pods(ns)
	for {
		pod := ""
		if err != nil {
			return err
		}
		pod, err = co.WaitForReadyPodForSelectorLabels(client, ns, selector, false)
		if err != nil {
			return err
		}
		if pod == "" {
			return fmt.Errorf("No pod found for namespace %s with selector %v", ns, selector)
		}
		err = co.TailLogs(ns, pod, containerName)
		if err != nil {
			return nil
		}
		podResource, err := podInterface.Get(pod, metav1.GetOptions{})
		if err != nil {
			return errors.Wrapf(err, "failed to get pod %s in namespace %s", pod, ns)
		}
		if kube.IsPodCompleted(podResource) {
			log.Logger().Infof("the Job pod %s has completed successfully", pod)
			return nil
		}
		log.Logger().Warnf("Job pod %s is not completed but has status: %s", pod, kube.PodStatus(podResource))
	}
}

// Git lazily create a gitter if its not specified
func (o *RunOptions) Git() gits.Gitter {
	if o.Gitter == nil {
		o.Gitter = gits.NewGitCLI()
	}
	return o.Gitter
}

// findRequirementsAndGitURL tries to find the current boot configuration from the cluster
func (o *RunOptions) findRequirementsAndGitURL() (*config.RequirementsConfig, string, error) {
	if o.JXFactory == nil {
		o.JXFactory = jxfactory.NewFactory()
	}

	var requirements *config.RequirementsConfig
	gitURL := ""

	jxClient, ns, err := o.JXFactory.CreateJXClient()
	if err != nil {
		return requirements, gitURL, err
	}
	devEnv, err := kube.GetDevEnvironment(jxClient, ns)
	if err != nil && !apierrors.IsNotFound(err) {
		return requirements, gitURL, err
	}
	if devEnv != nil {
		gitURL = devEnv.Spec.Source.URL
		requirements, err = config.GetRequirementsConfigFromTeamSettings(&devEnv.Spec.TeamSettings)
		if err != nil {
			log.Logger().Debugf("failed to load requirements from team settings %s", err.Error())
		}
	}
	if o.GitURL != "" {
		gitURL = o.GitURL
		if requirements == nil {
			requirements, err = reqhelpers.GetRequirementsFromGit(gitURL)
			if err != nil {
				return requirements, gitURL, errors.Wrapf(err, "failed to get requirements from git URL %s", gitURL)
			}
		}
	}

	if requirements == nil {
		requirements, _, err = config.LoadRequirementsConfig(o.Dir)
		if err != nil {
			return requirements, gitURL, err
		}
	}

	if gitURL == "" {
		// lets try find the git URL from
		gitURL, err = o.findGitURLFromDir()
		if err != nil {
			return requirements, gitURL, errors.Wrapf(err, "your cluster has not been booted before and you are not inside a git clone of your dev environment repository so you need to pass in the URL of the git repository as --git-url")
		}
	}
	return requirements, gitURL, nil
}

func (o *RunOptions) findGitURLFromDir() (string, error) {
	dir := o.Dir
	_, gitConfDir, err := o.Git().FindGitConfigDir(dir)
	if err != nil {
		return "", errors.Wrapf(err, "there was a problem obtaining the git config dir of directory %s", dir)
	}

	if gitConfDir == "" {
		return "", fmt.Errorf("no .git directory could be found from dir %s", dir)
	}
	return o.Git().DiscoverUpstreamGitURL(gitConfDir)
}

func (o *RunOptions) verifyBootSecret(requirements *config.RequirementsConfig) error {
	kubeClient, ns, err := o.JXFactory.CreateKubeClient()
	if err != nil {
		return errors.Wrap(err, "failed to create kube client")
	}

	reqNs := requirements.Cluster.Namespace
	if reqNs != "" && reqNs != ns {
		return errors.Errorf("you are currently in the %s namespace but this cluster needs to be booted in namespace %s. please use 'jx ns %s' to switch namespace", ns, reqNs, reqNs)
	}

	name := secretmgr.LocalSecret
	secret, err := kubeClient.CoreV1().Secrets(ns).Get(name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			warnNoSecret(ns, name)
			return fmt.Errorf("boot secret %s not found in namespace %s. are you sure you are running this command in the right namespace and cluster", name, ns)
		}
		warnNoSecret(ns, name)
		return errors.Wrapf(err, "failed to look for boot secret %s  in namespace %s", name, ns)
	}

	if secret == nil {
		return fmt.Errorf("null boot secret %s found in namespace %s. are you sure you are running this command in the right namespace and cluster", name, ns)
	}

	key := "secrets.yaml"
	found := false
	if secret.Data != nil {
		data := secret.Data[key]
		if len(data) > 0 {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("boot secret %s in namespace %s does not contain key: %s", name, ns, key)
	}
	return nil
}

func (o *RunOptions) findChartVersion(req *config.RequirementsConfig) (string, error) {
	if o.ChartName == "" || o.ChartName[0] == '.' || o.ChartName[0] == '/' || o.ChartName[0] == '\\' || strings.Count(o.ChartName, "/") > 1 {
		// relative chart folder so ignore version
		return "", nil
	}

	f := clients.NewFactory()
	co := opts.NewCommonOptionsWithTerm(f, os.Stdin, os.Stdout, os.Stderr)
	co.BatchMode = o.BatchMode

	u := req.VersionStream.URL
	ref := req.VersionStream.Ref
	version, err := co.GetVersionNumber(versionstream.KindChart, o.ChartName, u, ref)
	if err != nil {
		return version, errors.Wrapf(err, "failed to find version of chart %s in version stream %s ref %s", o.ChartName, u, ref)
	}
	return version, nil
}

func warnNoSecret(ns, name string) {
	log.Logger().Warnf("boot secret %s not found in namespace %s\n", name, ns)
	log.Logger().Infof("Are you running in the correct namespace? To change namespaces see:     %s", util.ColorInfo("https://jenkins-x.io/docs/using-jx/developing/kube-context/"))
	log.Logger().Infof("Did you remember to import or edit the secrets before running boot? see %s", util.ColorInfo("https://jenkins-x.io/docs/labs/boot/getting-started/secrets/"))
}

// IsInCluster tells if we are running incluster
func IsInCluster() bool {
	_, err := rest.InClusterConfig()
	return err == nil
}
