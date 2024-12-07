// GENERATED CODE -- DO NOT EDIT!

'use strict';
var grpc = require('@grpc/grpc-js');
var avs_pb = require('./avs_pb.js');
var google_protobuf_timestamp_pb = require('google-protobuf/google/protobuf/timestamp_pb.js');
var google_protobuf_wrappers_pb = require('google-protobuf/google/protobuf/wrappers_pb.js');

function serialize_aggregator_AckMessageReq(arg) {
  if (!(arg instanceof avs_pb.AckMessageReq)) {
    throw new Error('Expected argument of type aggregator.AckMessageReq');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_AckMessageReq(buffer_arg) {
  return avs_pb.AckMessageReq.deserializeBinary(new Uint8Array(buffer_arg));
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

function serialize_aggregator_NotifyTriggersReq(arg) {
  if (!(arg instanceof avs_pb.NotifyTriggersReq)) {
    throw new Error('Expected argument of type aggregator.NotifyTriggersReq');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_NotifyTriggersReq(buffer_arg) {
  return avs_pb.NotifyTriggersReq.deserializeBinary(new Uint8Array(buffer_arg));
}

function serialize_aggregator_NotifyTriggersResp(arg) {
  if (!(arg instanceof avs_pb.NotifyTriggersResp)) {
    throw new Error('Expected argument of type aggregator.NotifyTriggersResp');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_NotifyTriggersResp(buffer_arg) {
  return avs_pb.NotifyTriggersResp.deserializeBinary(new Uint8Array(buffer_arg));
}

function serialize_aggregator_SyncMessagesReq(arg) {
  if (!(arg instanceof avs_pb.SyncMessagesReq)) {
    throw new Error('Expected argument of type aggregator.SyncMessagesReq');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_SyncMessagesReq(buffer_arg) {
  return avs_pb.SyncMessagesReq.deserializeBinary(new Uint8Array(buffer_arg));
}

function serialize_aggregator_SyncMessagesResp(arg) {
  if (!(arg instanceof avs_pb.SyncMessagesResp)) {
    throw new Error('Expected argument of type aggregator.SyncMessagesResp');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_aggregator_SyncMessagesResp(buffer_arg) {
  return avs_pb.SyncMessagesResp.deserializeBinary(new Uint8Array(buffer_arg));
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
  // Task Management
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
};

exports.AggregatorClient = grpc.makeGenericClientConstructor(AggregatorService);
var NodeService = exports.NodeService = {
  // Operator endpoint
ping: {
    path: '/aggregator.Node/Ping',
    requestStream: false,
    responseStream: false,
    requestType: avs_pb.Checkin,
    responseType: avs_pb.CheckinResp,
    requestSerialize: serialize_aggregator_Checkin,
    requestDeserialize: deserialize_aggregator_Checkin,
    responseSerialize: serialize_aggregator_CheckinResp,
    responseDeserialize: deserialize_aggregator_CheckinResp,
  },
  syncMessages: {
    path: '/aggregator.Node/SyncMessages',
    requestStream: false,
    responseStream: true,
    requestType: avs_pb.SyncMessagesReq,
    responseType: avs_pb.SyncMessagesResp,
    requestSerialize: serialize_aggregator_SyncMessagesReq,
    requestDeserialize: deserialize_aggregator_SyncMessagesReq,
    responseSerialize: serialize_aggregator_SyncMessagesResp,
    responseDeserialize: deserialize_aggregator_SyncMessagesResp,
  },
  ack: {
    path: '/aggregator.Node/Ack',
    requestStream: false,
    responseStream: false,
    requestType: avs_pb.AckMessageReq,
    responseType: google_protobuf_wrappers_pb.BoolValue,
    requestSerialize: serialize_aggregator_AckMessageReq,
    requestDeserialize: deserialize_aggregator_AckMessageReq,
    responseSerialize: serialize_google_protobuf_BoolValue,
    responseDeserialize: deserialize_google_protobuf_BoolValue,
  },
  notifyTriggers: {
    path: '/aggregator.Node/NotifyTriggers',
    requestStream: false,
    responseStream: false,
    requestType: avs_pb.NotifyTriggersReq,
    responseType: avs_pb.NotifyTriggersResp,
    requestSerialize: serialize_aggregator_NotifyTriggersReq,
    requestDeserialize: deserialize_aggregator_NotifyTriggersReq,
    responseSerialize: serialize_aggregator_NotifyTriggersResp,
    responseDeserialize: deserialize_aggregator_NotifyTriggersResp,
  },
};

exports.NodeClient = grpc.makeGenericClientConstructor(NodeService);
