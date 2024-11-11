// GENERATED CODE -- DO NOT EDIT!

'use strict';
var grpc = require('@grpc/grpc-js');
var avs_pb = require('./avs_pb.js');
var google_protobuf_timestamp_pb = require('google-protobuf/google/protobuf/timestamp_pb.js');
var google_protobuf_wrappers_pb = require('google-protobuf/google/protobuf/wrappers_pb.js');

function serialize_aggregator_AddressRequest(arg) {
  if (!(arg instanceof avs_pb.AddressRequest)) {
    throw new Error('Expected argument of type aggregator.AddressRequest');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_AddressRequest(buffer_arg) {
  return avs_pb.AddressRequest.deserializeBinary(new Uint8Array(buffer_arg));
}

function serialize_aggregator_AddressResp(arg) {
  if (!(arg instanceof avs_pb.AddressResp)) {
    throw new Error('Expected argument of type aggregator.AddressResp');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_AddressResp(buffer_arg) {
  return avs_pb.AddressResp.deserializeBinary(new Uint8Array(buffer_arg));
}

function serialize_aggregator_Checkin(arg) {
  if (!(arg instanceof avs_pb.Checkin)) {
    throw new Error('Expected argument of type aggregator.Checkin');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_Checkin(buffer_arg) {
  return avs_pb.Checkin.deserializeBinary(new Uint8Array(buffer_arg));
}

function serialize_aggregator_CheckinResp(arg) {
  if (!(arg instanceof avs_pb.CheckinResp)) {
    throw new Error('Expected argument of type aggregator.CheckinResp');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_CheckinResp(buffer_arg) {
  return avs_pb.CheckinResp.deserializeBinary(new Uint8Array(buffer_arg));
}

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

function serialize_aggregator_CreateWalletReq(arg) {
  if (!(arg instanceof avs_pb.CreateWalletReq)) {
    throw new Error('Expected argument of type aggregator.CreateWalletReq');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_CreateWalletReq(buffer_arg) {
  return avs_pb.CreateWalletReq.deserializeBinary(new Uint8Array(buffer_arg));
}

function serialize_aggregator_CreateWalletResp(arg) {
  if (!(arg instanceof avs_pb.CreateWalletResp)) {
    throw new Error('Expected argument of type aggregator.CreateWalletResp');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_CreateWalletResp(buffer_arg) {
  return avs_pb.CreateWalletResp.deserializeBinary(new Uint8Array(buffer_arg));
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

function serialize_aggregator_KeyResp(arg) {
  if (!(arg instanceof avs_pb.KeyResp)) {
    throw new Error('Expected argument of type aggregator.KeyResp');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_KeyResp(buffer_arg) {
  return avs_pb.KeyResp.deserializeBinary(new Uint8Array(buffer_arg));
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

function serialize_aggregator_SyncTasksReq(arg) {
  if (!(arg instanceof avs_pb.SyncTasksReq)) {
    throw new Error('Expected argument of type aggregator.SyncTasksReq');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_SyncTasksReq(buffer_arg) {
  return avs_pb.SyncTasksReq.deserializeBinary(new Uint8Array(buffer_arg));
}

function serialize_aggregator_SyncTasksResp(arg) {
  if (!(arg instanceof avs_pb.SyncTasksResp)) {
    throw new Error('Expected argument of type aggregator.SyncTasksResp');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_SyncTasksResp(buffer_arg) {
  return avs_pb.SyncTasksResp.deserializeBinary(new Uint8Array(buffer_arg));
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

function serialize_aggregator_UUID(arg) {
  if (!(arg instanceof avs_pb.UUID)) {
    throw new Error('Expected argument of type aggregator.UUID');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_UUID(buffer_arg) {
  return avs_pb.UUID.deserializeBinary(new Uint8Array(buffer_arg));
}

function serialize_aggregator_UpdateChecksReq(arg) {
  if (!(arg instanceof avs_pb.UpdateChecksReq)) {
    throw new Error('Expected argument of type aggregator.UpdateChecksReq');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_UpdateChecksReq(buffer_arg) {
  return avs_pb.UpdateChecksReq.deserializeBinary(new Uint8Array(buffer_arg));
}

function serialize_aggregator_UpdateChecksResp(arg) {
  if (!(arg instanceof avs_pb.UpdateChecksResp)) {
    throw new Error('Expected argument of type aggregator.UpdateChecksResp');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_UpdateChecksResp(buffer_arg) {
  return avs_pb.UpdateChecksResp.deserializeBinary(new Uint8Array(buffer_arg));
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
  // Auth
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
  // Smart Acccount
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
  getSmartAccountAddress: {
    path: '/aggregator.Aggregator/GetSmartAccountAddress',
    requestStream: false,
    responseStream: false,
    requestType: avs_pb.AddressRequest,
    responseType: avs_pb.AddressResp,
    requestSerialize: serialize_aggregator_AddressRequest,
    requestDeserialize: deserialize_aggregator_AddressRequest,
    responseSerialize: serialize_aggregator_AddressResp,
    responseDeserialize: deserialize_aggregator_AddressResp,
  },
  // Task Management
createWallet: {
    path: '/aggregator.Aggregator/CreateWallet',
    requestStream: false,
    responseStream: false,
    requestType: avs_pb.CreateWalletReq,
    responseType: avs_pb.CreateWalletResp,
    requestSerialize: serialize_aggregator_CreateWalletReq,
    requestDeserialize: deserialize_aggregator_CreateWalletReq,
    responseSerialize: serialize_aggregator_CreateWalletResp,
    responseDeserialize: deserialize_aggregator_CreateWalletResp,
  },
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
    requestType: avs_pb.UUID,
    responseType: avs_pb.Task,
    requestSerialize: serialize_aggregator_UUID,
    requestDeserialize: deserialize_aggregator_UUID,
    responseSerialize: serialize_aggregator_Task,
    responseDeserialize: deserialize_aggregator_Task,
  },
  cancelTask: {
    path: '/aggregator.Aggregator/CancelTask',
    requestStream: false,
    responseStream: false,
    requestType: avs_pb.UUID,
    responseType: google_protobuf_wrappers_pb.BoolValue,
    requestSerialize: serialize_aggregator_UUID,
    requestDeserialize: deserialize_aggregator_UUID,
    responseSerialize: serialize_google_protobuf_BoolValue,
    responseDeserialize: deserialize_google_protobuf_BoolValue,
  },
  deleteTask: {
    path: '/aggregator.Aggregator/DeleteTask',
    requestStream: false,
    responseStream: false,
    requestType: avs_pb.UUID,
    responseType: google_protobuf_wrappers_pb.BoolValue,
    requestSerialize: serialize_aggregator_UUID,
    requestDeserialize: deserialize_aggregator_UUID,
    responseSerialize: serialize_google_protobuf_BoolValue,
    responseDeserialize: deserialize_google_protobuf_BoolValue,
  },
  // Operator endpoint
ping: {
    path: '/aggregator.Aggregator/Ping',
    requestStream: false,
    responseStream: false,
    requestType: avs_pb.Checkin,
    responseType: avs_pb.CheckinResp,
    requestSerialize: serialize_aggregator_Checkin,
    requestDeserialize: deserialize_aggregator_Checkin,
    responseSerialize: serialize_aggregator_CheckinResp,
    responseDeserialize: deserialize_aggregator_CheckinResp,
  },
  syncTasks: {
    path: '/aggregator.Aggregator/SyncTasks',
    requestStream: false,
    responseStream: true,
    requestType: avs_pb.SyncTasksReq,
    responseType: avs_pb.SyncTasksResp,
    requestSerialize: serialize_aggregator_SyncTasksReq,
    requestDeserialize: deserialize_aggregator_SyncTasksReq,
    responseSerialize: serialize_aggregator_SyncTasksResp,
    responseDeserialize: deserialize_aggregator_SyncTasksResp,
  },
  updateChecks: {
    path: '/aggregator.Aggregator/UpdateChecks',
    requestStream: false,
    responseStream: false,
    requestType: avs_pb.UpdateChecksReq,
    responseType: avs_pb.UpdateChecksResp,
    requestSerialize: serialize_aggregator_UpdateChecksReq,
    requestDeserialize: deserialize_aggregator_UpdateChecksReq,
    responseSerialize: serialize_aggregator_UpdateChecksResp,
    responseDeserialize: deserialize_aggregator_UpdateChecksResp,
  },
};

exports.AggregatorClient = grpc.makeGenericClientConstructor(AggregatorService);
