package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/google/go-github/github"
	"github.com/jomei/notionapi"
	"golang.org/x/oauth2"
)

type repoMigrator struct {
	githubClient   *github.Client
	notionClient   notionapi.Client
	githubOrgOwner string
	repoName       string
}

func main() {
	ctx := context.Background()

	notionToken := flag.String("notionToken", "", "token for notion api")
	githubToken := flag.String("githubToken", "", "token for github api")
	databaseID := flag.String("dbID", "", "id of notion DB")
	dryRun := flag.Bool("dryRun", true, "should save the pages or not")
	githubOrgOwner := flag.String("owner", "", "org owner")
	repoName := flag.String("repo", "", "name of repo")
	flag.Parse()

	if notionToken == nil || githubToken == nil || databaseID == nil || githubOrgOwner == nil || repoName == nil {
		fmt.Errorf("notion and github tokens are required")
		return
	}

	migrator := &repoMigrator{
		githubOrgOwner: *githubOrgOwner,
		repoName:       *repoName,
	}

	notionClient := notionapi.NewClient(notionapi.Token(*notionToken))
	migrator.notionClient = *notionClient

	db, err := notionClient.Database.Get(ctx, notionapi.DatabaseID(*databaseID))
	if err != nil {
		fmt.Errorf("issue getting notion db %s", err.Error())
		return
	}
	if db == nil {
		fmt.Errorf("could not find db")
		return
	}

	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: *githubToken},
	)
	tc := oauth2.NewClient(ctx, ts)

	githubClient := github.NewClient(tc)
	migrator.githubClient = githubClient

	prs := []*github.PullRequest{}

	// list all repositories for the authenticated user
	openOptions := &github.PullRequestListOptions{State: "open"}
	openOptions.ListOptions.PerPage = 100
	openOptions.ListOptions.Page = 0
	for {
		openPRs, resp, err := githubClient.PullRequests.List(ctx, *githubOrgOwner, *repoName, openOptions)
		if err != nil {
			fmt.Errorf("error listing PRs %s", err.Error())
			return
		}
		prs = append(prs, openPRs...)

		if resp.NextPage == 0 {
			break
		}
		openOptions.Page = resp.NextPage
	}

	closedOptions := &github.PullRequestListOptions{State: "closed"}
	closedOptions.ListOptions.PerPage = 100
	closedOptions.ListOptions.Page = 0
	for {
		closedPrs, resp, err := githubClient.PullRequests.List(ctx, *githubOrgOwner, *repoName, closedOptions)
		if err != nil {
			fmt.Errorf("error listing closed PRs %s", err.Error())
			return
		}
		prs = append(prs, closedPrs...)

		if resp.NextPage == 0 {
			break
		}
		closedOptions.Page = resp.NextPage
	}

	fmt.Printf("\nprocessing %v prs", len(prs))

	for _, pr := range prs {
		properties, err := migrator.formatNotionProperties(ctx, githubClient, pr)
		if err != nil {
			fmt.Errorf(err.Error())
			return
		}

		// get comments
		commentOptions := &github.IssueListCommentsOptions{}
		commentOptions.PerPage = 100
		comments, _, err := githubClient.Issues.ListComments(ctx, *githubOrgOwner, *repoName, *pr.Number, commentOptions)
		if err != nil {
			fmt.Errorf(err.Error())
			return
		}

		reviewCommentOptions := &github.PullRequestListCommentsOptions{}
		reviewCommentOptions.PerPage = 100
		reviewComments, _, err := githubClient.PullRequests.ListComments(ctx, *githubOrgOwner, *repoName, *pr.Number, reviewCommentOptions)

		body := setupPageBody(*pr.HTMLURL, *pr.Body)

		reviewCommentsHeader := notionapi.Heading2Block{
			Object: notionapi.ObjectTypeBlock,
			Type:   "heading_2",
			Heading2: notionapi.Heading{
				Text: []notionapi.RichText{
					{Text: notionapi.Text{Content: "Review comments"}},
				},
			},
		}

		if len(reviewComments) > 0 {
			body = append(body, reviewCommentsHeader)

			for _, comment := range reviewComments {
				body = append(body, githubCommentToNotionParagraph(*comment.Body, *comment.User.Login, *comment.CreatedAt, *comment.HTMLURL))
			}
		}

		inlineCommentsHeader := notionapi.Heading2Block{
			Object: notionapi.ObjectTypeBlock,
			Type:   "heading_2",
			Heading2: notionapi.Heading{
				Text: []notionapi.RichText{
					{Text: notionapi.Text{Content: "In-line comments"}},
				},
			},
		}

		if len(comments) > 0 {
			body = append(body, inlineCommentsHeader)

			for _, comment := range comments {
				note := githubCommentToNotionParagraph(*comment.Body, *comment.User.Login, *comment.CreatedAt, *comment.HTMLURL)
				body = append(body, note)
			}
		}

		if dryRun != nil && *dryRun == false {
			if err := createPage(ctx, notionClient, databaseID, properties, body); err != nil {
				fmt.Errorf("could not create page %s", err.Error())
				return
			}
		}
	}
}

func (rm *repoMigrator) formatNotionProperties(ctx context.Context, githubClient *github.Client, pr *github.PullRequest) (map[string]notionapi.Property, error) {
	properties := make(map[string]notionapi.Property)
	properties["Name"] = notionapi.TitleProperty{
		Type: "title",
		Title: []notionapi.RichText{
			{Text: notionapi.Text{Content: *pr.Title}},
		},
	}

	status := notionapi.Option{Name: "Approved", Color: notionapi.ColorGreen}
	if pr.ClosedAt != nil && pr.MergedAt == nil {
		status = notionapi.Option{Name: "Shelved", Color: notionapi.ColorRed}
	}
	if *pr.State == "open" {
		// we cannot handle draft PRs
		status = notionapi.Option{Name: "Feedback Requested", Color: notionapi.ColorBrown}
	}
	properties["Status"] = notionapi.MultiSelectProperty{
		Type: "multi_select",
		MultiSelect: []notionapi.Option{
			status,
		},
	}

	properties["Type"] = notionapi.MultiSelectProperty{
		Type: "multi_select",
		MultiSelect: []notionapi.Option{
			{Name: "legacy", Color: notionapi.ColorBlue},
		},
	}

	createdAt := notionapi.Date(*pr.CreatedAt)
	properties["Created At"] = notionapi.DateProperty{
		Type: "date",
		Date: notionapi.DateObject{
			Start: &createdAt,
		},
	}

	if pr.User != nil && pr.User.Login != nil {
		properties["Driver"] = notionapi.RichTextProperty{
			Type: "rich_text",
			RichText: []notionapi.RichText{
				{Type: "text", Text: notionapi.Text{Content: *pr.User.Login}},
			},
		}
	}

	reviewerListOptions := &github.ListOptions{}
	reviewerListOptions.PerPage = 100
	reviews, _, err := rm.githubClient.PullRequests.ListReviews(ctx, rm.githubOrgOwner, rm.repoName, *pr.Number, reviewerListOptions)
	if err != nil {
		return nil, fmt.Errorf("could not fetch reviewers %s", err.Error())
	}

	uniqueInformed := make(map[string]string)

	uniqueAccountable := make(map[string]string)
	accountable := []string{}
	for _, review := range reviews {
		// only add approvers to list of accountable
		if *review.State == "APPROVED" {
			uniqueAccountable[*review.User.Login] = *review.User.Login
		} else {
			// everyone else has only been informed
			uniqueInformed[*review.User.Login] = *review.User.Login
		}
	}

	for _, usr := range uniqueAccountable {
		accountable = append(accountable, usr)
	}
	properties["Accountable"] = notionapi.RichTextProperty{
		Type: "rich_text",
		RichText: []notionapi.RichText{
			{
				Type: "text",
				Text: notionapi.Text{Content: strings.Join(accountable, ", ")},
			},
		},
	}

	uniqueContributors := make(map[string]string)
	for _, usr := range pr.RequestedReviewers {
		if usr.Login != nil {
			uniqueContributors[*usr.Login] = *usr.Login
		}
	}
	contributors := []string{}
	for _, uniqueContributor := range uniqueContributors {
		contributors = append(contributors, uniqueContributor)
	}
	properties["Contributors"] = notionapi.RichTextProperty{
		Type: "rich_text",
		RichText: []notionapi.RichText{
			{Type: "text", Text: notionapi.Text{Content: strings.Join(contributors, ", ")}},
		},
	}

	servicesSurfaces := []notionapi.Option{}
	for _, label := range pr.Labels {
		switch *label.Name {
		case "data":
			uniqueInformed["data"] = "data"
		case "desktop":
			servicesSurfaces = append(servicesSurfaces, notionapi.Option{Name: "desktop", Color: notionapi.ColorBlue})
			uniqueInformed["guild-surfaces"] = "guild-surfaces"
		case "marketplace-core":
			uniqueInformed["monetization"] = "monetization"
		case "search":
			uniqueInformed["search"] = "search"
		case "sig-backend":
			uniqueInformed["guild-api"] = "guild-api"
		case "sre":
			uniqueInformed["sre"] = "sre"
		case "studio":
			uniqueInformed["ltb"] = "ltb"
		case "surfaces":
			uniqueInformed["guild-surfaces"] = "guild-surfaces"
		case "vert-cc":
			uniqueInformed["ltb"] = "ltb"
		case "vert-gear":
			uniqueInformed["creator tools"] = "creator tools"
			uniqueInformed["monetization"] = "monetization"
		case "vert-sounds":
			uniqueInformed["catalog"] = "catalog"
		}
	}

	informed := []string{}
	for _, uniqueInformed := range uniqueInformed {
		informed = append(informed, uniqueInformed)
	}
	properties["Informed"] = notionapi.RichTextProperty{
		Type: "rich_text",
		RichText: []notionapi.RichText{
			{
				Type: "text",
				Text: notionapi.Text{Content: strings.Join(informed, ", ")},
			},
		},
	}
	properties["Services/Surfaces"] = notionapi.MultiSelectProperty{
		Type:        "multi_select",
		MultiSelect: servicesSurfaces,
	}

	return properties, nil
}

func createPage(ctx context.Context, notionClient *notionapi.Client, databaseID *string, properties notionapi.Properties, body []notionapi.Block) error {
	page, err := notionClient.Page.Create(ctx, &notionapi.PageCreateRequest{
		Parent: notionapi.Parent{
			DatabaseID: notionapi.DatabaseID(*databaseID), // ID of the RFC/ADR database
		},
		Properties: properties,
		Children:   body,
	})
	if err != nil {
		fmt.Printf("\nerror creating page %+v", err)
		return err
	}
	fmt.Printf("\ncreated page %s", page.URL)
	return nil
}

func setupPageBody(prURL string, prBody string) []notionapi.Block {
	return []notionapi.Block{
		notionapi.Heading1Block{
			Object: notionapi.ObjectTypeBlock,
			Type:   "heading_1",
			Heading1: notionapi.Heading{
				Text: []notionapi.RichText{
					{Text: notionapi.Text{Content: "URL to Original PR", Link: &notionapi.Link{Url: prURL}}},
				},
			},
		},
		notionapi.Heading1Block{
			Object: notionapi.ObjectTypeBlock,
			Type:   "heading_1",
			Heading1: notionapi.Heading{
				Text: []notionapi.RichText{
					{Text: notionapi.Text{Content: "Description"}},
				},
			},
		},
		notionapi.ParagraphBlock{
			Object: notionapi.ObjectTypeBlock,
			Type:   "paragraph",
			Paragraph: notionapi.Paragraph{
				Text: []notionapi.RichText{
					{Text: notionapi.Text{Content: prBody}},
				},
			},
		},
		notionapi.Heading1Block{
			Object: notionapi.ObjectTypeBlock,
			Type:   "heading_1",
			Heading1: notionapi.Heading{
				Text: []notionapi.RichText{
					{Text: notionapi.Text{Content: "Comments"}},
				},
			},
		},
	}
}

func githubCommentToNotionParagraph(commentBody string, commentUser string, commentCreatedAt time.Time, commentURL string) notionapi.ParagraphBlock {
	runeBody := []rune(commentBody)
	body := commentBody
	if len(runeBody) > 2000 {
		body = string(runeBody[:1990]) + "..."
	}

	return notionapi.ParagraphBlock{
		Object: notionapi.ObjectTypeBlock,
		Type:   "paragraph",
		Paragraph: notionapi.Paragraph{
			Text: []notionapi.RichText{
				{
					Type: notionapi.ObjectTypeText,
					Text: notionapi.Text{
						Content: "🗣️ ",
					},
				},
				{
					Type: notionapi.ObjectTypeText,
					Text: notionapi.Text{
						Content: commentUser + fmt.Sprintf(" on %d-%02d-%02d at %02d:%02d: ", commentCreatedAt.Year(), commentCreatedAt.Month(), commentCreatedAt.Day(), commentCreatedAt.Hour(), commentCreatedAt.Minute()),
						Link:    &notionapi.Link{Url: commentURL},
					},
				},
				{
					Type: notionapi.ObjectTypeText,
					Text: notionapi.Text{
						Content: body,
					},
				},
			},
		},
	}
}
