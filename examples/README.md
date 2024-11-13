# Ava Protocol Examples

Example codes on how to interact with Ava Protocol RPC server to create and
manage tasks.

Examples weren't written to be parameterized or extensible. Its only purpose
is to show how to run a specific example, and allow the audience to see
how the code will look like.

Therefore, the script is harded coded, there is no parameter to provide or anything.

If you need to change a parameter for a test, edit the code and re-run it.

# Available example

## Prepare depedencies

```
npm ci
```

Then run:

```
node example.js
```

it will list all available action to run.

## Setting env

```
export env=<development|staging|production>
export PRIVATE_KEY=<any-wallet-private-key>
```

The test example using a dummy token which anyone can mint https://sepolia.etherscan.io/address/0x2e8bdb63d09ef989a0018eeb1c47ef84e3e61f7b#writeProxyContract
