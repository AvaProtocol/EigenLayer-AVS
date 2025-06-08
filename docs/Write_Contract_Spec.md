Contract **write functions** have very different response structures compared to read functions. Here are the key differences and examples:

## **Key Differences:**

- **Read functions**: Return data immediately (view/pure functions)
- **Write functions**: Return transaction hash, require gas, change blockchain state

## **Write Function Response Examples:**

### **1. Successful Write Transaction:**

```json
{
  "results": [
    {
      "method_name": "transfer",
      "success": true,
      "transaction": {
        "hash": "0x1f9b58e1f4c8e9b6b7f8c4d3c9c8e7d6f5e4d3c2b1a9f8e7d6c5b4a3f2e1d0c9",
        "status": "pending",
        "block_number": null,
        "gas_used": null,
        "gas_limit": "21000",
        "gas_price": "20000000000",
        "from": "0x742d35Cc6Af4d8f5b5aA9E1c3C3e8d5A8b7C6d4e",
        "to": "0xA0b86a33E6441b53dBe1c8eF4D2b6c4e3c7d5f6e",
        "value": "0",
        "nonce": 42,
        "timestamp": 1733142580
      },
      "events": [],
      "input_data": "0xa9059cbb000000000000000000000000742d35cc6af4d8f5b5aa9e1c3c3e8d5a8b7c6d4e0000000000000000000000000000000000000000000000000de0b6b3a7640000"
    }
  ]
}
```

### **2. Confirmed Write Transaction (after mining):**

```json
{
  "results": [
    {
      "method_name": "transfer",
      "success": true,
      "transaction": {
        "hash": "0x1f9b58e1f4c8e9b6b7f8c4d3c9c8e7d6f5e4d3c2b1a9f8e7d6c5b4a3f2e1d0c9",
        "status": "confirmed",
        "block_number": "18500234",
        "block_hash": "0x2a8c96b5e4d7f9c1b3e6a8f2d5c9e7b4f1a6d8c3e9b2f5a7d1c4e8b6f3a9d2c5",
        "gas_used": "21000",
        "gas_limit": "21000", 
        "gas_price": "20000000000",
        "effective_gas_price": "20000000000",
        "from": "0x742d35Cc6Af4d8f5b5aA9E1c3C3e8d5A8b7C6d4e",
        "to": "0xA0b86a33E6441b53dBe1c8eF4D2b6c4e3c7d5f6e",
        "value": "0",
        "nonce": 42,
        "transaction_index": 15,
        "confirmations": 12,
        "timestamp": 1733142580
      },
      "events": [
        {
          "event_name": "Transfer",
          "address": "0xA0b86a33E6441b53dBe1c8eF4D2b6c4e3c7d5f6e",
          "topics": [
            "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
            "0x000000000000000000000000742d35cc6af4d8f5b5aa9e1c3c3e8d5a8b7c6d4e",
            "0x0000000000000000000000005aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed"
          ],
          "data": "0x0000000000000000000000000000000000000000000000000de0b6b3a7640000",
          "decoded": {
            "from": "0x742d35Cc6Af4d8f5b5aA9E1c3C3e8d5A8b7C6d4e",
            "to": "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed",
            "value": "1000000000000000000"
          }
        }
      ],
      "return_data": {
        "name": "",
        "type": "bool",
        "value": "true"
      }
    }
  ]
}
```

### **3. Failed Write Transaction:**

```json
{
  "results": [
    {
      "method_name": "transfer",
      "success": false,
      "transaction": {
        "hash": "0x3c7d5e2a8b9f1e4c6d7a9b3e5f8c2d4a6e9b1f3c5a7d9e2b4f6c8a1d3e5b7f9",
        "status": "failed",
        "block_number": "18500235",
        "gas_used": "21000",
        "gas_limit": "21000",
        "gas_price": "20000000000",
        "from": "0x742d35Cc6Af4d8f5b5aA9E1c3C3e8d5A8b7C6d4e",
        "to": "0xA0b86a33E6441b53dBe1c8eF4D2b6c4e3c7d5f6e",
        "value": "0",
        "nonce": 43,
        "timestamp": 1733142650
      },
      "error": {
        "code": "EXECUTION_REVERTED",
        "message": "ERC20: transfer amount exceeds balance",
        "revert_reason": "0x08c379a00000000000000000000000000000000000000000000000000000000000000020000000000000000000000000000000000000000000000000000000000000002645524332303a207472616e7366657220616d6f756e7420657863656564732062616c616e63650000000000000000000000000000000000000000000000000000"
      },
      "events": []
    }
  ]
}
```

### **4. Multi-Function Write Response:**

```json
{
  "results": [
    {
      "method_name": "approve",
      "success": true,
      "transaction": {
        "hash": "0x4d8e6f3a9c2b5e7f1a4d6c8b9e2f5a7d3c6e9b1f4a7d2c5e8b3f6a9d1c4e7b2",
        "status": "confirmed",
        "block_number": "18500240",
        "gas_used": "46123",
        "gas_limit": "50000",
        "gas_price": "25000000000"
      },
      "events": [
        {
          "event_name": "Approval",
          "decoded": {
            "owner": "0x742d35Cc6Af4d8f5b5aA9E1c3C3e8d5A8b7C6d4e",
            "spender": "0x1F98431c8aD98523631AE4a59f267346ea31F984",
            "value": "115792089237316195423570985008687907853269984665640564039457584007913129639935"
          }
        }
      ],
      "return_data": {
        "name": "",
        "type": "bool", 
        "value": "true"
      }
    },
    {
      "method_name": "swap",
      "success": true,
      "transaction": {
        "hash": "0x5e9f7a4b8d1c3e6f2a5d8c7b4e9f2a6d5c8e1f4a7d3c6e9b2f5a8d1c4e7b3f6",
        "status": "confirmed", 
        "block_number": "18500241",
        "gas_used": "184561",
        "gas_limit": "200000",
        "gas_price": "25000000000"
      },
      "events": [
        {
          "event_name": "Swap",
          "decoded": {
            "sender": "0x742d35Cc6Af4d8f5b5aA9E1c3C3e8d5A8b7C6d4e",
            "amount0In": "1000000000000000000",
            "amount1In": "0",
            "amount0Out": "0", 
            "amount1Out": "1945230000",
            "to": "0x742d35Cc6Af4d8f5b5aA9E1c3C3e8d5A8b7C6d4e"
          }
        }
      ]
    }
  ]
}
```

## **Key Response Fields for Write Functions:**

### **Transaction Data:**
- **`hash`**: Unique transaction identifier
- **`status`**: "pending", "confirmed", "failed"
- **`gas_used`**: Actual gas consumed
- **`gas_price`**: Gas price paid
- **`block_number`**: Block where transaction was mined

### **Events/Logs:**
- **`events`**: Array of emitted events
- **`decoded`**: Human-readable event data
- **`topics`**: Raw event topics (indexed parameters)

### **Error Handling:**
- **`error.code`**: Error type (EXECUTION_REVERTED, OUT_OF_GAS, etc.)
- **`error.message`**: Human-readable error
- **`revert_reason`**: Encoded revert message

## **Summary:**

**Read functions** return data directly, while **write functions** return transaction details, events, and confirmation status. Write responses are much more complex due to the blockchain state changes involved.