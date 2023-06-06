## Edit Discourse topics locally

The discedit tool allows you to edit [Discourse](https://www.discourse.org/) topics in your favourite local text editor. It works by pulling a topic from Discourse, opening it in a local text editor and automatically pushing the edits.

### Requirements

discedit requires:

* [Go](https://golang.org/doc/install), installed locally
* a Discourse API key for the instance you want access to (see below)


## Get started

### Install discedit

**1. Clone this repository and navigate into the directory:**
```
git clone https://github.com/niemeyer/discedit.git
cd discedit
```

**2. Compile the application:**
```
go build
```


### Configure discedit with your Discourse key(s)

For each Discourse instance that you want to interact with, you need to create an API key in the Discourse admin. Select:

* **User Level**: *Single User*
* **Global Key (allows all actions)**

If you don't have the rights to handle keys, the administrator of your Discourse installation will need to create the key for you.

Create a file `~/.discedit` in the following format:

```
forums:
    https://some.discourse.domain:
        username: your-username
        key: your-key
```

### Edit a topic with discedit

In the directory where you built discedit, run:

```
./discedit <forum topic URL>
```

The topic will pull down the topic as a file, and open it in your system's preferred editor. When you close the file, discedit will push the content (if it has changed) back to Discourse.


## Refinements

### Use live edit mode

The `-live-edit` option will push your changes to Discourse on save, not just on closing the file (this can also be included in any alias you set up):

```
discedit -live-edit <forum topic URL>
```

### Use the clipboard

Rather than having to paste the topic URL each time, you can read it straight from the clipboard (note use of single quotes to ensure that the commands are expanded when the alias is used, rather than when it's created):

For Linux: `discedit '$(xclip -o -selection -c)'`

For macOS: `discedit '$(pbpaste)'`

### Add an alias

It's more convenient to be able to run discedit from anywhere, not just the `discedit` repository. It's also more convenient to set your preferred command line options. Add an alias, for example:

For Linux: `alias discedit='~/Repositories/discedit/discedit -live-edit $(xclip -o -selection -c)'`
For macOS: `alias discedit='~/Repositories/discedit/discedit -live-edit $(pbpaste)'`

## Reference

discedit options are:

* `-debug`: Debug mode
* `-force-draft`: Open draft even if it has conflicts
* `-ignore-draft`: Ignore existing draft and start over
* `-live-edit`: Update post while content is being edited
