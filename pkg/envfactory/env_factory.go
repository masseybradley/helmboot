package envfactory

import (
	"context"
	"fmt"
	"io/ioutil"

	"github.com/jenkins-x-labs/helmboot/pkg/jxadapt"
	"github.com/jenkins-x-labs/helmboot/pkg/reqhelpers"
	"github.com/jenkins-x/go-scm/scm"
	"github.com/jenkins-x/jx/pkg/auth"
	"github.com/jenkins-x/jx/pkg/config"
	"github.com/jenkins-x/jx/pkg/gits"
	"github.com/jenkins-x/jx/pkg/jxfactory"
	"github.com/jenkins-x/jx/pkg/log"
	"github.com/jenkins-x/jx/pkg/util"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

type EnvFactory struct {
	JXFactory     jxfactory.Factory
	RepoName      string
	GitURLOutFile string
	BatchMode     bool
	Gitter        gits.Gitter
	IOFileHandles *util.IOFileHandles
	ScmClient     *scm.Client
	OutDir        string
}

// AddFlags adds common CLI flags
func (o *EnvFactory) AddFlags(cmd *cobra.Command) {
	cmd.Flags().BoolVarP(&o.BatchMode, "batch-mode", "b", false, "Enables batch mode which avoids prompting for user input")
	cmd.Flags().StringVarP(&o.RepoName, "repo", "", "", "the name of the development git repository to create")
	cmd.Flags().StringVarP(&o.GitURLOutFile, "out", "", "", "the name of the file to save with the created git URL inside")

}

// CreateDevEnvGitRepository creates the dev environment git repository from the given directory
func (o *EnvFactory) CreateDevEnvGitRepository(dir string) error {
	o.OutDir = dir
	requirements, fileName, err := config.LoadRequirementsConfig(dir)
	if err != nil {
		return errors.Wrapf(err, "failed to load requirements from %s", dir)
	}

	dev := reqhelpers.GetDevEnvironmentConfig(requirements)
	if dev == nil {
		return fmt.Errorf("the file %s does not contain a development environment", fileName)
	}

	cr := &CreateRepository{
		GitServer:  requirements.Cluster.GitServer,
		GitKind:    requirements.Cluster.GitKind,
		Owner:      dev.Owner,
		Repository: dev.Repository,
	}
	if cr.Owner == "" {
		cr.Owner = requirements.Cluster.EnvironmentGitOwner
	}
	if cr.Repository == "" {
		cr.Repository = o.RepoName
	}

	handles := jxadapt.ToIOHandles(o.IOFileHandles)
	err = cr.ConfirmValues(o.BatchMode, handles)
	if err != nil {
		return err
	}

	scmClient, token, err := o.JXAdapter().ScmClient(cr.GitServer, cr.Owner, cr.GitKind)
	if err != nil {
		return errors.Wrapf(err, "failed to create SCM client for server %s", cr.GitServer)
	}
	o.ScmClient = scmClient

	user, _, err := scmClient.Users.Find(context.Background())
	if err != nil {
		return errors.Wrap(err, "failed to find the current SCM user")
	}
	cr.CurrentUsername = user.Login

	userAuth := &auth.UserAuth{
		Username: user.Login,
		ApiToken: token,
	}
	repo, err := cr.CreateRepository(scmClient)
	if err != nil {
		return err
	}
	err = o.pushToRepository(dir, repo, userAuth)
	if err != nil {
		return errors.Wrap(err, "failed to push to the git repository")
	}
	err = o.PrintBootJobInstructions(requirements, repo.Link)
	if err != nil {
		return err
	}
	if o.GitURLOutFile != "" {
		err = ioutil.WriteFile(o.GitURLOutFile, []byte(repo.Link), util.DefaultFileWritePermissions)
		if err != nil {
			return errors.Wrapf(err, "failed to save Git URL to file %s", o.GitURLOutFile)
		}
	}
	return nil
}

// PrintBootJobInstructions prints the instructions to run the installer
func (o *EnvFactory) PrintBootJobInstructions(requirements *config.RequirementsConfig, link string) error {

	log.Logger().Info("\nto boot your cluster run the following commands:\n\n")

	info := util.ColorInfo
	log.Logger().Infof("%s", info("helmboot secrets edit"))
	log.Logger().Infof("%s", info(fmt.Sprintf("helmboot run --git-url %s", link)))
	return nil
}

func (o *EnvFactory) pushToRepository(dir string, repo *scm.Repository, userAuth *auth.UserAuth) error {
	cloneURL := repo.Clone

	forkPushURL, err := o.Gitter.CreateAuthenticatedURL(cloneURL, userAuth)
	if err != nil {
		return errors.Wrapf(err, "creating push URL for %s", cloneURL)
	}

	remoteBranch := "master"
	err = o.Gitter.Push(dir, forkPushURL, true, fmt.Sprintf("%s:%s", "HEAD", remoteBranch))
	if err != nil {
		return errors.Wrapf(err, "pushing merged branch %s", remoteBranch)
	}

	log.Logger().Infof("pushed code to the repository")
	return nil
}

// JXAdapter creates an adapter to the jx code
func (o *EnvFactory) JXAdapter() *jxadapt.JXAdapter {
	return jxadapt.NewJXAdapter(o.JXFactory, o.Gitter, o.BatchMode)
}
