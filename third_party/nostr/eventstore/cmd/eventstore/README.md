# eventstore command-line tool

```
go install fiatjaf.com/nostr/eventstore/cmd/eventstore@latest
```

## Usage

This should be pretty straightforward. You pipe events or filters, as JSON, to the `eventstore` command, and they yield something. You can use [nak](https://github.com/fiatjaf/nak) to generate these events or filters easily.

### Querying the last 100 events of kind 1

```fish
~> nak req -k 1 -l 100 --bare | eventstore -d /path/to/store query
~> # or
~> echo '{"kinds":[1],"limit":100}' | eventstore -d /path/to/store query
```

This will automatically determine the storage type being used at `/path/to/store`, but you can also specify it manually using the `-t` option (`-t lmdb` etc).

### Saving an event to the store

```fish
~> nak event -k 1 -c hello | eventstore -d /path/to/store save
~> # or
~> echo '{"id":"35369e6bae5f77c4e1745c2eb5db84c4493e87f6e449aee62a261bbc1fea2788","pubkey":"79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798","created_at":1701193836,"kind":1,"tags":[],"content":"hello","sig":"ef08d559e042d9af4cdc3328a064f737603d86ec4f929f193d5a3ce9ea22a3fb8afc1923ee3c3742fd01856065352c5632e91f633528c80e9c5711fa1266824c"}' | eventstore -d /path/to/store save
```

### Counting events matching a filter

```fish
~> echo '{"kinds":[1]}' | eventstore -d /path/to/store count
```

### Deleting an event by ID

```fish
~> echo '35369e6bae5f77c4e1745c2eb5db84c4493e87f6e449aee62a261bbc1fea2788' | eventstore -d /path/to/store delete
```

### Query or save (default command)

Pipes events or filters and handles them appropriately.

You can also create a database from scratch if it's a disk database, but then you have to specify `-t` to `boltdb` or `lmdb`.

Supported store types: `lmdb`, `boltdb`, `mmm`, `file` (for JSONL files).

