// GENERATED CODE -- DO NOT EDIT!

'use strict';
var grpc = require('@grpc/grpc-js');
var avs_pb = require('./avs_pb.js');
var google_protobuf_wrappers_pb = require('google-protobuf/google/protobuf/wrappers_pb.js');

function serialize_aggregator_CreateTaskReq(arg) {
  if (!(arg instanceof avs_pb.CreateTaskReq)) {
    throw new Error('Expected argument of type aggregator.CreateTaskReq');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_CreateTaskReq(buffer_arg) {
  return avs_pb.CreateTaskReq.deserializeBinary(new Uint8Array(buffer_arg));
}

function serialize_aggregator_CreateTaskResp(arg) {
  if (!(arg instanceof avs_pb.CreateTaskResp)) {
    throw new Error('Expected argument of type aggregator.CreateTaskResp');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_CreateTaskResp(buffer_arg) {
  return avs_pb.CreateTaskResp.deserializeBinary(new Uint8Array(buffer_arg));
}

function serialize_aggregator_GetKeyReq(arg) {
  if (!(arg instanceof avs_pb.GetKeyReq)) {
    throw new Error('Expected argument of type aggregator.GetKeyReq');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_GetKeyReq(buffer_arg) {
  return avs_pb.GetKeyReq.deserializeBinary(new Uint8Array(buffer_arg));
}

function serialize_aggregator_GetWalletReq(arg) {
  if (!(arg instanceof avs_pb.GetWalletReq)) {
    throw new Error('Expected argument of type aggregator.GetWalletReq');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_GetWalletReq(buffer_arg) {
  return avs_pb.GetWalletReq.deserializeBinary(new Uint8Array(buffer_arg));
}

function serialize_aggregator_GetWalletResp(arg) {
  if (!(arg instanceof avs_pb.GetWalletResp)) {
    throw new Error('Expected argument of type aggregator.GetWalletResp');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_GetWalletResp(buffer_arg) {
  return avs_pb.GetWalletResp.deserializeBinary(new Uint8Array(buffer_arg));
}

function serialize_aggregator_IdReq(arg) {
  if (!(arg instanceof avs_pb.IdReq)) {
    throw new Error('Expected argument of type aggregator.IdReq');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_IdReq(buffer_arg) {
  return avs_pb.IdReq.deserializeBinary(new Uint8Array(buffer_arg));
}

function serialize_aggregator_KeyResp(arg) {
  if (!(arg instanceof avs_pb.KeyResp)) {
    throw new Error('Expected argument of type aggregator.KeyResp');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_KeyResp(buffer_arg) {
  return avs_pb.KeyResp.deserializeBinary(new Uint8Array(buffer_arg));
}

function serialize_aggregator_ListExecutionsReq(arg) {
  if (!(arg instanceof avs_pb.ListExecutionsReq)) {
    throw new Error('Expected argument of type aggregator.ListExecutionsReq');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_ListExecutionsReq(buffer_arg) {
  return avs_pb.ListExecutionsReq.deserializeBinary(new Uint8Array(buffer_arg));
}

function serialize_aggregator_ListExecutionsResp(arg) {
  if (!(arg instanceof avs_pb.ListExecutionsResp)) {
    throw new Error('Expected argument of type aggregator.ListExecutionsResp');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_ListExecutionsResp(buffer_arg) {
  return avs_pb.ListExecutionsResp.deserializeBinary(new Uint8Array(buffer_arg));
}

function serialize_aggregator_ListTasksReq(arg) {
  if (!(arg instanceof avs_pb.ListTasksReq)) {
    throw new Error('Expected argument of type aggregator.ListTasksReq');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_ListTasksReq(buffer_arg) {
  return avs_pb.ListTasksReq.deserializeBinary(new Uint8Array(buffer_arg));
}

function serialize_aggregator_ListTasksResp(arg) {
  if (!(arg instanceof avs_pb.ListTasksResp)) {
    throw new Error('Expected argument of type aggregator.ListTasksResp');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_ListTasksResp(buffer_arg) {
  return avs_pb.ListTasksResp.deserializeBinary(new Uint8Array(buffer_arg));
}

function serialize_aggregator_ListWalletReq(arg) {
  if (!(arg instanceof avs_pb.ListWalletReq)) {
    throw new Error('Expected argument of type aggregator.ListWalletReq');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_ListWalletReq(buffer_arg) {
  return avs_pb.ListWalletReq.deserializeBinary(new Uint8Array(buffer_arg));
}

function serialize_aggregator_ListWalletResp(arg) {
  if (!(arg instanceof avs_pb.ListWalletResp)) {
    throw new Error('Expected argument of type aggregator.ListWalletResp');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_ListWalletResp(buffer_arg) {
  return avs_pb.ListWalletResp.deserializeBinary(new Uint8Array(buffer_arg));
}

function serialize_aggregator_NonceRequest(arg) {
  if (!(arg instanceof avs_pb.NonceRequest)) {
    throw new Error('Expected argument of type aggregator.NonceRequest');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_NonceRequest(buffer_arg) {
  return avs_pb.NonceRequest.deserializeBinary(new Uint8Array(buffer_arg));
}

function serialize_aggregator_NonceResp(arg) {
  if (!(arg instanceof avs_pb.NonceResp)) {
    throw new Error('Expected argument of type aggregator.NonceResp');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_NonceResp(buffer_arg) {
  return avs_pb.NonceResp.deserializeBinary(new Uint8Array(buffer_arg));
}

function serialize_aggregator_Task(arg) {
  if (!(arg instanceof avs_pb.Task)) {
    throw new Error('Expected argument of type aggregator.Task');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_Task(buffer_arg) {
  return avs_pb.Task.deserializeBinary(new Uint8Array(buffer_arg));
}

function serialize_aggregator_UserTriggerTaskReq(arg) {
  if (!(arg instanceof avs_pb.UserTriggerTaskReq)) {
    throw new Error('Expected argument of type aggregator.UserTriggerTaskReq');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_UserTriggerTaskReq(buffer_arg) {
  return avs_pb.UserTriggerTaskReq.deserializeBinary(new Uint8Array(buffer_arg));
}

function serialize_aggregator_UserTriggerTaskResp(arg) {
  if (!(arg instanceof avs_pb.UserTriggerTaskResp)) {
    throw new Error('Expected argument of type aggregator.UserTriggerTaskResp');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_UserTriggerTaskResp(buffer_arg) {
  return avs_pb.UserTriggerTaskResp.deserializeBinary(new Uint8Array(buffer_arg));
}

function serialize_google_protobuf_BoolValue(arg) {
  if (!(arg instanceof google_protobuf_wrappers_pb.BoolValue)) {
    throw new Error('Expected argument of type google.protobuf.BoolValue');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_google_protobuf_BoolValue(buffer_arg) {
  return google_protobuf_wrappers_pb.BoolValue.deserializeBinary(new Uint8Array(buffer_arg));
}


var AggregatorService = exports.AggregatorService = {
  // Exchange for an Auth Key to authenticate in subsequent request
getKey: {
    path: '/aggregator.Aggregator/GetKey',
    requestStream: false,
    responseStream: false,
    requestType: avs_pb.GetKeyReq,
    responseType: avs_pb.KeyResp,
    requestSerialize: serialize_aggregator_GetKeyReq,
    requestDeserialize: deserialize_aggregator_GetKeyReq,
    responseSerialize: serialize_aggregator_KeyResp,
    responseDeserialize: deserialize_aggregator_KeyResp,
  },
  // Smart Acccount Operation
getNonce: {
    path: '/aggregator.Aggregator/GetNonce',
    requestStream: false,
    responseStream: false,
    requestType: avs_pb.NonceRequest,
    responseType: avs_pb.NonceResp,
    requestSerialize: serialize_aggregator_NonceRequest,
    requestDeserialize: deserialize_aggregator_NonceRequest,
    responseSerialize: serialize_aggregator_NonceResp,
    responseDeserialize: deserialize_aggregator_NonceResp,
  },
  getWallet: {
    path: '/aggregator.Aggregator/GetWallet',
    requestStream: false,
    responseStream: false,
    requestType: avs_pb.GetWalletReq,
    responseType: avs_pb.GetWalletResp,
    requestSerialize: serialize_aggregator_GetWalletReq,
    requestDeserialize: deserialize_aggregator_GetWalletReq,
    responseSerialize: serialize_aggregator_GetWalletResp,
    responseDeserialize: deserialize_aggregator_GetWalletResp,
  },
  listWallets: {
    path: '/aggregator.Aggregator/ListWallets',
    requestStream: false,
    responseStream: false,
    requestType: avs_pb.ListWalletReq,
    responseType: avs_pb.ListWalletResp,
    requestSerialize: serialize_aggregator_ListWalletReq,
    requestDeserialize: deserialize_aggregator_ListWalletReq,
    responseSerialize: serialize_aggregator_ListWalletResp,
    responseDeserialize: deserialize_aggregator_ListWalletResp,
  },
  // Task Management Operation
createTask: {
    path: '/aggregator.Aggregator/CreateTask',
    requestStream: false,
    responseStream: false,
    requestType: avs_pb.CreateTaskReq,
    responseType: avs_pb.CreateTaskResp,
    requestSerialize: serialize_aggregator_CreateTaskReq,
    requestDeserialize: deserialize_aggregator_CreateTaskReq,
    responseSerialize: serialize_aggregator_CreateTaskResp,
    responseDeserialize: deserialize_aggregator_CreateTaskResp,
  },
  listTasks: {
    path: '/aggregator.Aggregator/ListTasks',
    requestStream: false,
    responseStream: false,
    requestType: avs_pb.ListTasksReq,
    responseType: avs_pb.ListTasksResp,
    requestSerialize: serialize_aggregator_ListTasksReq,
    requestDeserialize: deserialize_aggregator_ListTasksReq,
    responseSerialize: serialize_aggregator_ListTasksResp,
    responseDeserialize: deserialize_aggregator_ListTasksResp,
  },
  getTask: {
    path: '/aggregator.Aggregator/GetTask',
    requestStream: false,
    responseStream: false,
    requestType: avs_pb.IdReq,
    responseType: avs_pb.Task,
    requestSerialize: serialize_aggregator_IdReq,
    requestDeserialize: deserialize_aggregator_IdReq,
    responseSerialize: serialize_aggregator_Task,
    responseDeserialize: deserialize_aggregator_Task,
  },
  listExecutions: {
    path: '/aggregator.Aggregator/ListExecutions',
    requestStream: false,
    responseStream: false,
    requestType: avs_pb.ListExecutionsReq,
    responseType: avs_pb.ListExecutionsResp,
    requestSerialize: serialize_aggregator_ListExecutionsReq,
    requestDeserialize: deserialize_aggregator_ListExecutionsReq,
    responseSerialize: serialize_aggregator_ListExecutionsResp,
    responseDeserialize: deserialize_aggregator_ListExecutionsResp,
  },
  cancelTask: {
    path: '/aggregator.Aggregator/CancelTask',
    requestStream: false,
    responseStream: false,
    requestType: avs_pb.IdReq,
    responseType: google_protobuf_wrappers_pb.BoolValue,
    requestSerialize: serialize_aggregator_IdReq,
    requestDeserialize: deserialize_aggregator_IdReq,
    responseSerialize: serialize_google_protobuf_BoolValue,
    responseDeserialize: deserialize_google_protobuf_BoolValue,
  },
  deleteTask: {
    path: '/aggregator.Aggregator/DeleteTask',
    requestStream: false,
    responseStream: false,
    requestType: avs_pb.IdReq,
    responseType: google_protobuf_wrappers_pb.BoolValue,
    requestSerialize: serialize_aggregator_IdReq,
    requestDeserialize: deserialize_aggregator_IdReq,
    responseSerialize: serialize_google_protobuf_BoolValue,
    responseDeserialize: deserialize_google_protobuf_BoolValue,
  },
  triggerTask: {
    path: '/aggregator.Aggregator/TriggerTask',
    requestStream: false,
    responseStream: false,
    requestType: avs_pb.UserTriggerTaskReq,
    responseType: avs_pb.UserTriggerTaskResp,
    requestSerialize: serialize_aggregator_UserTriggerTaskReq,
    requestDeserialize: deserialize_aggregator_UserTriggerTaskReq,
    responseSerialize: serialize_aggregator_UserTriggerTaskResp,
    responseDeserialize: deserialize_aggregator_UserTriggerTaskResp,
  },
};

exports.AggregatorClient = grpc.makeGenericClientConstructor(AggregatorService);
