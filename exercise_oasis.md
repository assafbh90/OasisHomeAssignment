IdentityHub - Exercise

IdentityHub - NHI Management Platform

Background

IdentityHub is a Non-Human Identity
NHI
management platform. Organizations use our product to
track and manage their service accounts, API keys, service principals, and other machine identities
across cloud environments.
Our customers have requested a way to quickly create Jira tickets when they discover identity-related
issues (e.g., stale service accounts, overprivileged keys, expiring credentials). Your task is to build a
proof-of-concept integration that allows users to report NHI findings directly to their Jira workspace.

Technical Requirements
Your solution should be runnable in the easiest, most frictionless way possible. You define the stack.

We'll be assessing the following areas:
Code architecture — Clear separation between UI and backend layers. We'll review the codebase to
ensure it's clean and well-organized.
Security practices — Proper handling of multi-tenancy and credential management, following secure
coding standards.
Product thinking — Evidence that you've considered the end-user experience. Error messages should
be clear and meaningful, and interactions should feel intuitive.
Scope definition — You have flexibility to determine which Jira projects and field options to support.
Document your decisions so we can evaluate your reasoning and understanding of the problem space.
Architectural knowledge - understand the choices made.

Functional Requirements
1. Jira Integration + Authentication
User Login to the app + Logout
Secure session management
The application must support multiple concurrent users without data interference
Ability to integrate with Jira after logon.
You can create a free Jira account at
2. Create NHI Finding Ticket
UI

User selects / writes a Jira project from their connected workspace
User fills out a form to create an NHI finding ticket:
Title (summary) — e.g., "Stale Service Account: svc-deploy-prod"
Description — details about the finding
3. Recent Tickets View
Display the 10 most recent tickets (that were created from this app) from the selected project
Show ticket title and creation time
Each ticket should be clickable and open the corresponding Jira issue in a new tab\
. REST API
Expose an API endpoint that allows external systems (e.g., scanners, CI/CD pipelines) to
programmatically create NHI finding tickets.
The endpoint should:
Follow REST conventions
Require API key
Return appropriate status codes and error messages
Validate input data

Tip
Use AI properly to design and write the code - but you are the owner eventually, so understand the
solution.

Bonus Challenge

Optional)

Build an automation feature: "NHI Blog Digest"
atlassian.com

Create an automation that:
. Fetches the most recent blog post from
. Generates an AI-powered summary of the post
. Automatically creates a Jira ticket with:
The blog post title
The AI-generated summary
Any trigger / scheduled can work - your choice -
Note: This is external to the UI, you don’t need to add a support on the UI For that.

Submission
. Provide a full compressed file of the project
. Include setup instructions and any assumptions or design decisions
. Send us the repository link

Questions?
If anything is unclear, make reasonable assumptions and document them. We're interested in seeing
how you approach ambiguity.
Good luck! 🚀
