# Oak Automation On Ethereum

Oak + EigenLayer

# Oak AVS

Currently one have to compile the code directly. Using go 10.22

```
go build
```

### Run operator

First register your AVS, and then run the operator

```
oak-avs register 
oak-avs run-operator
```

### Run aggregrator

```
oak-avs run-aggregrator
```

Note: currently aggregrator will be run by Oak team. The IP address for
communication between operator and aggregrator will be hardcode in the operator.

# How it works

<table><tr><td bgcolor='white'><img src="docs/highlevel-diagram.png"/></td></tr></table>


## User wallet

For each owner we deploy a ERC6900 wallet to schedule task and approve spending
to user wallet.

Each task type has their equivalent modular code to re-present their condition
and their actual execution.

## Aggregator

Aggregator accepts RPC request from client to submit Task Payload. Currently, aggregrator is managed and run by Oak team.

Periodcally, aggregrator combine the task submission, update our internal
storage and a zkSNARK proof will be write back to our TaskManager contract.

Aggregator also accept task condition check result from operator, perform quorum
and consensus check, then write the result back and flag that a task is good to
run.

## Operators

Operators communicates with aggregrators through RPC. It requests task data from aggregrator, it performs condition execution to check whether a task can be trigger. The result is then send back to aggregrator.

For task is ok to run, the operator will executed them. The detail of how task
is triggering throuhg our ERC6900 modular wallet will come soon.

# Development guide

## Dependencies

### eigenlayer cli

```
curl -sSfL https://raw.githubusercontent.com/layr-labs/eigenlayer-cli/master/scripts/install.sh | sh -s
```

### golang

```
brew install go
```

### foundry toolchain

```
curl -L https://foundry.paradigm.xyz | bash
foundryup
```

## Getting started

Coming soon

## Contract address

### Holesky Testnet

TaskManager Proof - 
Service Manager - 


### Ethereum Mainnet

TaskManager Proof - 
Service Manager - 
