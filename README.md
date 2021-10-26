## Github => Notion RFC Migration Tool

This a small tool that pulls all PRs from a specific Github repo then reformats them to fit into the Notion style and uploads them to a specific database on a Notion page creating a new page for each PR entry.

API Docs:
- [Github API](https://docs.github.com/en/rest/reference/pulls)
- [Notion API](https://developers.notion.com/reference/intro)

Requirements:
- Github token
- Notion integration

Usage:

`go run main.go -notionToken="<notion integration token>" -githubToken="<github token>" -dbID="<notion db id>" -dryRun=0 -githubOwner=<github repo owner> -repo=<github repo name>`