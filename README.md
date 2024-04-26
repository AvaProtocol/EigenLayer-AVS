# OAK Automation On Ethereum

OAK + EigenLayer

# OAK AVS

The OAK AVS can be compiled directly using Go version 10.22. Ensure you have the appropriate version of Go installed on your development environment.

Check GO version:

```
go version
```

Compile OAK AVS:

```
go build
```

### Run operator

To run the AVS operator, follow these steps:
1. Register your AVS by executing the following command:
	```
	oak-avs register 
	```
1. Start the operator:
	```
	oak-avs run-operator
	```

### Run aggregrator

To run the aggregator, use the following command:

```
oak-avs run-aggregrator
```

Note: The OAK team currently manages the aggregator, and the communication IP address between the operator and the aggregator is hardcoded in the operator.

# How it works

<table><tr><td bgcolor='white'><img src="docs/highlevel-diagram.png"/></td></tr></table>


## User wallet

For each owner we deploy a ERC6900 wallet to schedule task and approve spending
to user wallet.

Each task type has their equivalent modular code to re-present their condition
and their actual execution.

## Aggregator

Aggregator accepts RPC request from client to submit Task Payload. Currently, aggregrator is managed and run by OAK team.

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

### EigenLayer CLI

Install the EigenLayer CLI with the following command:

```
curl -sSfL https://raw.githubusercontent.com/layr-labs/eigenlayer-cli/master/scripts/install.sh | sh -s
```

### Golang

Install Go with the following command:

```
brew install go
```

### Foundry Toolchain

Install the Foundry toolchain with the following commands:

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
