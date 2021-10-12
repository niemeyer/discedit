## Edit Discourse topics locally
The discedit tool allows you to edit [Discourse](https://www.discourse.org/) topics in your favourite local text editor. It works by pulling a topic from Discourse, opening it in a local text editor and automatically pushing the edits. 

## Setting up
You will need to install [Go](https://golang.org/doc/install) and have a Discourse API key. The key should be personal (set to a single user) and the scope should be set to global (allows all actions). If you don't have the rights to handle keys, get in touch with the administrator of your Discourse installation.

**1. Clone this repository and navigate into the directory:**
```
git clone https://github.com/niemeyer/discedit.git
cd discedit
```

**2. Compile the application:**
```
go build
```

**3. Run and follow the tool instructions to complete your setup.**
```
./discedit <topic URL>
```
