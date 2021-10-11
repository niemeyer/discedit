## Edit Discourse posts locally
Discedit allows you to edit [Discourse](https://www.discourse.org/) posts in your favourite local text editor. It woks by pulling a post from Discourse, openning it in your favourite text editor and automatically pushing the edits. 

## Setting up
You will need to install [Go](https://golang.org/doc/install) and generate a personal global Discourse API key. If you don't have the rights to handle keys, talk to the administrator of your Discourse installation.

1. Clone this repository and navigate into the directory:

`git clone https://github.com/niemeyer/discedit.git`

`cd discedit`

2. Compile the application:

`go build`

3. Create a `.discedit` file (note the dot before `discedit`) in your `$HOME` directory and add the following:

``` yaml
forums:
    https://some.discourse.domain:
        username: your-username
        key: your-key
```

4. From the `discedit` directory, run:

`./discedit [post_url]`

If this is the first time you are runnning discedit, the application will ask you to choose a text editor. 


## Options:
```
  -debug
    	Debug mode
  -force-draft
    	Open draft even if it has conflicts
  -ignore-draft
    	Ignore existing draft and start over
  -live-edit
    	Update post while content is being edited
```
