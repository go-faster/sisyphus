package config

import "github.com/go-faster/figureout"

// describeGit declares git repository ingestion and the local file sources.
func describeGit(c *wire, s *figureout.Schema[wire]) {
	figureout.Group(s, "git", func(s *figureout.Schema[wire]) {
		figureout.Value(s, &c.Git.WorkDir, "work_dir").ApplyDefault("").
			Doc("where repositories are cloned")
		secret(s, &c.Secrets.GitToken, "token")
		figureout.ListOf(s, &c.Git.Repos, "repos", describeGitSource).MergeByKey("repo")
	})
	figureout.Ignore(s, &c.Git.Token, figureout.Reason("materialized from git.token"))

	figureout.ListOf(s, &c.ContextFiles, "context_files", func(e *ContextFileSource, s *figureout.Schema[ContextFileSource]) {
		figureout.Value(s, &e.Name, "name").NonEmpty()
		figureout.Value(s, &e.Root, "root").NonEmpty()
		figureout.Value(s, &e.BaseURL, "base_url").ApplyDefault("")
		figureout.Value(s, &e.Include, "include").ApplyDefault(nil)
		figureout.Value(s, &e.Exclude, "exclude").ApplyDefault(nil)
		figureout.Value(s, &e.Authority, "authority").ApplyDefault("")
	}).MergeByKey("name")
}

func describeGitSource(e *GitSource, s *figureout.Schema[GitSource]) {
	figureout.Value(s, &e.Root, "root").ApplyDefault("")
	figureout.Value(s, &e.URL, "url").ApplyDefault("")
	figureout.Value(s, &e.Repo, "repo").ApplyDefault("")
	figureout.Value(s, &e.Branch, "branch").ApplyDefault("")
	figureout.Value(s, &e.BaseURL, "base_url").ApplyDefault("")
	figureout.Value(s, &e.Include, "include").ApplyDefault(nil)
	figureout.Value(s, &e.Exclude, "exclude").ApplyDefault(nil)
	figureout.Value(s, &e.Commits, "commits").ApplyDefault(false)
	figureout.Value(s, &e.Tags, "tags").ApplyDefault(false)
	figureout.Value(s, &e.Manifests, "manifests").ApplyDefault(false)
	figureout.Value(s, &e.Code, "code").ApplyDefault(false)
	figureout.Value(s, &e.ManifestExclude, "manifest_exclude").ApplyDefault(nil)
	figureout.Value(s, &e.CodeInclude, "code_include").ApplyDefault(nil)
	figureout.Value(s, &e.CodeExclude, "code_exclude").ApplyDefault(nil)
}

// describeGitLab declares GitLab REST ingestion: issues, MRs and releases.
func describeGitLab(c *wire, s *figureout.Schema[wire]) {
	figureout.Group(s, "gitlab", func(s *figureout.Schema[wire]) {
		figureout.Value(s, &c.GitLab.BaseURL, "base_url").ApplyDefault("")
		secret(s, &c.Secrets.GitLabToken, "token")
		figureout.ListOf(s, &c.GitLab.Projects, "projects", func(e *GitLabProject, s *figureout.Schema[GitLabProject]) {
			figureout.Value(s, &e.Ref, "ref").NonEmpty().
				Doc("numeric id or full path, such as group/project")
		}).MergeByKey("ref")
		figureout.Value(s, &c.GitLab.Issues, "issues").ApplyDefault(false)
		figureout.Value(s, &c.GitLab.MergeRequests, "merge_requests").ApplyDefault(false)
		figureout.Value(s, &c.GitLab.Releases, "releases").ApplyDefault(false)

		figureout.Group(s, "webhook", func(s *figureout.Schema[wire]) {
			figureout.Value(s, &c.GitLab.WebhookEnabled, "enabled").ApplyDefault(false)
			secret(s, &c.Secrets.GitLabWebhookSecret, "secret")
		})
		figureout.Group(s, "poll", func(s *figureout.Schema[wire]) {
			figureout.Value(s, &c.GitLab.PollIntervalSeconds, "interval_seconds").ApplyDefault(0).
				Doc("0 disables polling; webhooks still work")
		})
	})
	figureout.Ignore(s, &c.GitLab.Token, figureout.Reason("materialized from gitlab.token"))
	figureout.Ignore(s, &c.GitLab.WebhookSecret, figureout.Reason("materialized from gitlab.webhook.secret"))
}

// describeJira declares Jira REST ingestion.
func describeJira(c *wire, s *figureout.Schema[wire]) {
	figureout.Group(s, "jira", func(s *figureout.Schema[wire]) {
		figureout.Value(s, &c.Jira.BaseURL, "base_url").ApplyDefault("")
		figureout.Value(s, &c.Jira.Email, "email").ApplyDefault("")
		secret(s, &c.Secrets.JiraUsername, "username")
		secret(s, &c.Secrets.JiraAPIToken, "api_token")
		secret(s, &c.Secrets.JiraPassword, "password")
		secret(s, &c.Secrets.JiraPAT, "pat")
		figureout.ListOf(s, &c.Jira.Projects, "projects", func(e *JiraProject, s *figureout.Schema[JiraProject]) {
			figureout.Value(s, &e.Key, "key").NonEmpty()
		}).MergeByKey("key")

		figureout.Group(s, "webhook", func(s *figureout.Schema[wire]) {
			figureout.Value(s, &c.Jira.WebhookEnabled, "enabled").ApplyDefault(false)
			secret(s, &c.Secrets.JiraWebhookSecret, "secret")
		})
		figureout.Group(s, "poll", func(s *figureout.Schema[wire]) {
			figureout.Value(s, &c.Jira.PollIntervalSeconds, "interval_seconds").ApplyDefault(0)
		})
	})
	figureout.Ignore(s, &c.Jira.Username, figureout.Reason("materialized from jira.username"))
	figureout.Ignore(s, &c.Jira.APIToken, figureout.Reason("materialized from jira.api_token"))
	figureout.Ignore(s, &c.Jira.Password, figureout.Reason("materialized from jira.password"))
	figureout.Ignore(s, &c.Jira.PAT, figureout.Reason("materialized from jira.pat"))
	figureout.Ignore(s, &c.Jira.WebhookSecret, figureout.Reason("materialized from jira.webhook.secret"))
}

// describeTelegram declares the MTProto side: the user session that backfills
// history and the bot that answers /context.
func describeTelegram(c *wire, s *figureout.Schema[wire]) {
	figureout.Group(s, "telegram", func(s *figureout.Schema[wire]) {
		figureout.Value(s, &c.Telegram.Addr, "addr").ApplyDefault(defaultBotAddr).
			Doc("ssbot's own health/ready server")
		figureout.Value(s, &c.Telegram.AppID, "app_id").ApplyDefault(0)
		secret(s, &c.Secrets.TelegramAppHash, "app_hash")
		secret(s, &c.Secrets.TelegramBotToken, "bot_token")
		figureout.Value(s, &c.Telegram.SessionDir, "session_dir").ApplyDefault("./session")
		figureout.Value(s, &c.Telegram.Silent, "silent").ApplyDefault(false)
		figureout.ListOf(s, &c.Telegram.MonitorChats, "monitor_chats", func(e *TelegramChat, s *figureout.Schema[TelegramChat]) {
			figureout.Value(s, &e.ID, "id").ApplyDefault(0)
			figureout.Value(s, &e.Username, "username").ApplyDefault("")
			figureout.Value(s, &e.Limit, "limit").ApplyDefault(0)
		}).MergeByKey("id")
		figureout.Value(s, &c.Telegram.IngestSession, "ingest_session").ApplyDefault("")
		figureout.Value(s, &c.Telegram.AllowedChats, "allowed_chats").ApplyDefault(nil).
			Doc("empty means the bot ignores every message")
		figureout.Value(s, &c.Telegram.AllowedUserIDs, "allowed_user_ids").ApplyDefault(nil)
		figureout.Value(s, &c.Telegram.AnswerTimeoutSeconds, "answer_timeout_seconds").ApplyDefault(60).
			Doc("binding ceiling for a Telegram user; see context.timeout_seconds")
	})
	figureout.Ignore(s, &c.Telegram.AppHash, figureout.Reason("materialized from telegram.app_hash"))
	figureout.Ignore(s, &c.Telegram.BotToken, figureout.Reason("materialized from telegram.bot_token"))
}
