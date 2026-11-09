// web/ is the UI's npm project, not Go code. This file makes it a separate module so that ./... in the repository
// root does not walk web/node_modules (some npm packages ship .go files).
module github.com/theatarr/theatarr/web

go 1.27
