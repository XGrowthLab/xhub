# Model and endpoint API — generated reference

Generated from OpenAPI; no production handler is replaced. See model-contract.md for verification boundaries.

[OpenAPI](../../../contracts/models/openapi.json)

| Operation | Behavior | HTTP statuses |
| --- | --- | --- |
| `GET /v2/model/info` | List deployments | 200, 401, 403 |
| `GET /model/groups` | List public model groups | 200, 401, 403, 500, 503 |
| `POST /model/new` | Create stored deployment | 200, 400, 401, 403, 409, 500 |
| `POST /model/update` | Merge deployment patch | 200, 400, 401, 403, 409, 500 |
| `PATCH /model/{model_id}/update` | Update deployment by path | 200, 400, 401, 403, 409, 500 |
| `POST /model/delete` | Delete deployment | 200, 400, 401, 403, 409, 500 |
| `POST /model/disable` | Disable deployment | 200, 400, 401, 403, 500 |
| `POST /model/enable` | Enable deployment | 200, 400, 401, 403, 500 |
| `POST /health/test_connection` | Probe stored deployment | 200, 400, 401, 403, 404 |
| `GET /price/deployments` | Compare deployed prices | 200, 401, 403, 500, 503 |
| `POST /price/deployments/apply` | Confirm single or bulk price changes | 200, 400, 401, 403, 409, 500, 503 |
| `POST /model/protocol_probe` | Detect protocols for a saved deployment | 200, 400, 401, 403, 404, 409, 503 |

### GET /v2/model/info

Platform admin only; unknown ID returns empty list; other legacy filters are ignored

```json
[
  {
    "in": "query",
    "name": "modelId",
    "schema": {
      "type": "string"
    },
    "description": "Existing string parsing retained"
  },
  {
    "in": "query",
    "name": "search",
    "schema": {
      "type": "string"
    },
    "description": "Existing string parsing retained"
  },
  {
    "in": "query",
    "name": "page",
    "schema": {
      "type": "string"
    },
    "description": "Existing string parsing retained"
  },
  {
    "in": "query",
    "name": "size",
    "schema": {
      "type": "string"
    },
    "description": "Existing string parsing retained"
  }
]
```


### GET /model/groups

List public model groups

```json
[
  {
    "in": "query",
    "name": "search",
    "schema": {
      "type": "string"
    },
    "description": "Existing string parsing retained"
  },
  {
    "in": "query",
    "name": "page",
    "schema": {
      "type": "string"
    },
    "description": "Existing string parsing retained"
  },
  {
    "in": "query",
    "name": "size",
    "schema": {
      "type": "string"
    },
    "description": "Existing string parsing retained"
  }
]
```


### POST /model/new

Declares supported input; cross-field checks remain in real service, no strict runtime middleware

```json
{
  "required": true,
  "content": {
    "application/json": {
      "schema": {
        "$ref": "#/components/schemas/CreateDeployment"
      }
    }
  }
}
```


### POST /model/update

Omitted fields retained; masked secrets preserved; ID from model_info.id or id

```json
{
  "required": true,
  "content": {
    "application/json": {
      "schema": {
        "$ref": "#/components/schemas/UpdateDeployment"
      }
    }
  }
}
```


### PATCH /model/{model_id}/update

Update deployment by path

```json
[
  {
    "in": "path",
    "name": "model_id",
    "required": true,
    "schema": {
      "type": "string"
    }
  }
]
```

```json
{
  "required": true,
  "content": {
    "application/json": {
      "schema": {
        "$ref": "#/components/schemas/UpdateDeployment"
      }
    }
  }
}
```


### POST /model/delete

Delete deployment

```json
{
  "required": true,
  "content": {
    "application/json": {
      "schema": {
        "$ref": "#/components/schemas/DeleteSelector"
      }
    }
  }
}
```


### POST /model/disable

Disable deployment

```json
{
  "required": true,
  "content": {
    "application/json": {
      "schema": {
        "$ref": "#/components/schemas/DeploymentSelector"
      }
    }
  }
}
```


### POST /model/enable

Enable deployment

```json
{
  "required": true,
  "content": {
    "application/json": {
      "schema": {
        "$ref": "#/components/schemas/DeploymentSelector"
      }
    }
  }
}
```


### POST /health/test_connection

Uses stored URL and credentials only; HTTP200 may be unhealthy; bypasses budget and billing

```json
{
  "required": true,
  "content": {
    "application/json": {
      "schema": {
        "$ref": "#/components/schemas/ConnectionRequest"
      }
    }
  }
}
```


### GET /price/deployments

Admin only; returns current prices, source candidates and versions. Complex or delisted prices cannot be applied automatically


### POST /price/deployments/apply

Admin only; submit unique deployment IDs and preview versions. Server rechecks prices; any conflict rolls the batch back with 409. Historical bills remain unchanged

```json
{
  "required": true,
  "content": {
    "application/json": {
      "schema": {
        "$ref": "#/components/schemas/DeploymentPriceSelection"
      }
    }
  }
}
```


### POST /model/protocol_probe

400: invalid probe configuration; 404: missing deployment; 409: busy or changed configuration; 503: storage unavailable.

```json
{
  "required": true,
  "content": {
    "application/json": {
      "schema": {
        "type": "object",
        "required": [
          "id"
        ],
        "properties": {
          "id": {
            "type": "string",
            "minLength": 1
          }
        },
        "additionalProperties": true
      }
    }
  }
}
```


## Schemas

### Parameters

```json
{
  "type": "object",
  "properties": {
    "model": {
      "type": "string"
    },
    "custom_llm_provider": {
      "type": "string"
    },
    "litellm_credential_name": {
      "type": "string"
    },
    "api_base": {
      "type": "string"
    },
    "api_key": {
      "type": "string"
    },
    "input_cost_per_token": {
      "anyOf": [
        {
          "type": "number",
          "format": "double",
          "minimum": 0
        },
        {
          "type": "string",
          "nullable": true
        }
      ],
      "description": "Finite nonnegative numeric strings or null accepted; service validates string values"
    },
    "output_cost_per_token": {
      "anyOf": [
        {
          "type": "number",
          "format": "double",
          "minimum": 0
        },
        {
          "type": "string",
          "nullable": true
        }
      ],
      "description": "Finite nonnegative numeric strings or null accepted; service validates string values"
    }
  },
  "required": [],
  "additionalProperties": true,
  "description": "Extensible upstream parameters; sensitive top-level strings are masked, not arbitrary nested fields",
  "x-go-type": "rawjson.RawMessage",
  "x-go-type-import": {
    "path": "encoding/json",
    "name": "rawjson"
  }
}
```

### ModelInfo

```json
{
  "type": "object",
  "properties": {
    "id": {
      "type": "string"
    },
    "transport": {
      "type": "string"
    },
    "endpoint_types": {
      "type": "array",
      "items": {
        "type": "string"
      }
    },
    "pricing_source": {
      "type": "string"
    },
    "base_model": {
      "type": "string"
    },
    "disabled": {
      "type": "boolean"
    },
    "db_model": {
      "type": "boolean"
    },
    "unavailable_reason": {
      "type": "string"
    },
    "auto_probe": {
      "type": "boolean",
      "description": "Detect saved connection changes; no periodic upstream calls"
    }
  },
  "required": [],
  "additionalProperties": true,
  "x-go-type": "rawjson.RawMessage",
  "x-go-type-import": {
    "path": "encoding/json",
    "name": "rawjson"
  }
}
```

### Deployment

```json
{
  "type": "object",
  "properties": {
    "model_name": {
      "type": "string"
    },
    "litellm_params": {
      "$ref": "#/components/schemas/Parameters"
    },
    "model_info": {
      "$ref": "#/components/schemas/PublicModelInfo"
    }
  },
  "required": [
    "model_name",
    "litellm_params",
    "model_info"
  ],
  "additionalProperties": false
}
```

### DeploymentRow

```json
{
  "type": "object",
  "properties": {
    "model_name": {
      "type": "string"
    },
    "litellm_params": {
      "$ref": "#/components/schemas/Parameters"
    },
    "model_info": {
      "$ref": "#/components/schemas/PublicModelInfo"
    },
    "id": {
      "type": "string"
    },
    "db_model": {
      "type": "boolean"
    }
  },
  "required": [
    "model_name",
    "litellm_params",
    "model_info",
    "id",
    "db_model"
  ],
  "additionalProperties": false
}
```

### DeletedDeployment

```json
{
  "type": "object",
  "properties": {
    "model_name": {
      "type": "string"
    },
    "litellm_params": {
      "$ref": "#/components/schemas/Parameters"
    },
    "model_info": {
      "$ref": "#/components/schemas/PublicModelInfo"
    },
    "id": {
      "type": "string"
    },
    "deleted": {
      "type": "boolean",
      "enum": [
        true
      ]
    }
  },
  "required": [
    "model_name",
    "litellm_params",
    "model_info",
    "id",
    "deleted"
  ],
  "additionalProperties": false
}
```

### DeploymentList

```json
{
  "type": "object",
  "properties": {
    "data": {
      "type": "array",
      "items": {
        "$ref": "#/components/schemas/DeploymentRow"
      }
    },
    "total_count": {
      "type": "integer",
      "minimum": 0
    },
    "current_page": {
      "type": "integer",
      "minimum": 1
    },
    "total_pages": {
      "type": "integer",
      "minimum": 1
    },
    "size": {
      "type": "integer",
      "minimum": 1
    }
  },
  "required": [
    "data",
    "total_count",
    "current_page",
    "total_pages",
    "size"
  ],
  "additionalProperties": false
}
```

### DefaultWeights

```json
{
  "type": "object",
  "properties": {
    "allocations": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "deployment_id": {
            "type": "string"
          },
          "weight": {
            "type": "number",
            "format": "double"
          }
        },
        "required": [
          "deployment_id",
          "weight"
        ],
        "additionalProperties": false
      },
      "nullable": true
    }
  },
  "required": [
    "allocations"
  ],
  "additionalProperties": false
}
```

### FallbackPolicy

```json
{
  "type": "object",
  "properties": {
    "fallbacks": {
      "type": "array",
      "items": {
        "type": "string"
      },
      "nullable": true
    },
    "context_window_fallbacks": {
      "type": "array",
      "items": {
        "type": "string"
      },
      "nullable": true
    },
    "content_policy_fallbacks": {
      "type": "array",
      "items": {
        "type": "string"
      },
      "nullable": true
    }
  },
  "required": [
    "fallbacks",
    "context_window_fallbacks",
    "content_policy_fallbacks"
  ],
  "additionalProperties": false
}
```

### ModelGroup

```json
{
  "type": "object",
  "properties": {
    "model_name": {
      "type": "string"
    },
    "deployments": {
      "type": "array",
      "items": {
        "$ref": "#/components/schemas/DeploymentRow"
      }
    },
    "default_weights": {
      "$ref": "#/components/schemas/DefaultWeights"
    },
    "fallback_policy": {
      "$ref": "#/components/schemas/FallbackPolicy"
    }
  },
  "required": [
    "model_name",
    "deployments",
    "default_weights",
    "fallback_policy"
  ],
  "additionalProperties": false
}
```

### GroupList

```json
{
  "type": "object",
  "properties": {
    "data": {
      "type": "array",
      "items": {
        "$ref": "#/components/schemas/ModelGroup"
      }
    },
    "total_count": {
      "type": "integer",
      "minimum": 0
    },
    "current_page": {
      "type": "integer",
      "minimum": 1
    },
    "total_pages": {
      "type": "integer",
      "minimum": 1
    }
  },
  "required": [
    "data",
    "total_count",
    "current_page",
    "total_pages"
  ],
  "additionalProperties": false
}
```

### CreateDeployment

```json
{
  "type": "object",
  "properties": {
    "model_name": {
      "type": "string"
    },
    "litellm_params": {
      "$ref": "#/components/schemas/Parameters"
    },
    "model_info": {
      "$ref": "#/components/schemas/ModelInfo"
    }
  },
  "required": [
    "model_name",
    "litellm_params",
    "model_info"
  ],
  "additionalProperties": true,
  "x-go-type": "rawjson.RawMessage",
  "x-go-type-import": {
    "path": "encoding/json",
    "name": "rawjson"
  }
}
```

### UpdateDeployment

```json
{
  "type": "object",
  "properties": {
    "id": {
      "type": "string"
    },
    "model_name": {
      "type": "string"
    },
    "litellm_params": {
      "$ref": "#/components/schemas/Parameters"
    },
    "model_info": {
      "$ref": "#/components/schemas/ModelInfo"
    },
    "disabled": {
      "type": "boolean"
    }
  },
  "required": [],
  "additionalProperties": true,
  "x-go-type": "rawjson.RawMessage",
  "x-go-type-import": {
    "path": "encoding/json",
    "name": "rawjson"
  }
}
```

### DeploymentSelector

```json
{
  "type": "object",
  "properties": {
    "id": {
      "type": "string"
    },
    "model": {
      "type": "string"
    },
    "model_name": {
      "type": "string"
    }
  },
  "required": [],
  "additionalProperties": true,
  "x-go-type": "rawjson.RawMessage",
  "x-go-type-import": {
    "path": "encoding/json",
    "name": "rawjson"
  },
  "anyOf": [
    {
      "type": "object",
      "properties": {
        "id": {
          "type": "string"
        }
      },
      "required": [
        "id"
      ]
    },
    {
      "type": "object",
      "properties": {
        "model": {
          "type": "string"
        }
      },
      "required": [
        "model"
      ]
    },
    {
      "type": "object",
      "properties": {
        "model_name": {
          "type": "string"
        }
      },
      "required": [
        "model_name"
      ]
    }
  ]
}
```

### ConnectionRequest

```json
{
  "type": "object",
  "properties": {
    "model_info": {
      "type": "object",
      "properties": {
        "id": {
          "type": "string"
        }
      },
      "required": [
        "id"
      ],
      "additionalProperties": true
    },
    "litellm_params": {
      "$ref": "#/components/schemas/Parameters"
    },
    "model": {
      "type": "string"
    },
    "mode": {
      "type": "string"
    }
  },
  "required": [
    "model_info"
  ],
  "additionalProperties": true,
  "x-go-type": "rawjson.RawMessage",
  "x-go-type-import": {
    "path": "encoding/json",
    "name": "rawjson"
  }
}
```

### ConnectionSuccess

```json
{
  "type": "object",
  "properties": {
    "status": {
      "type": "string",
      "enum": [
        "success"
      ]
    },
    "healthy": {
      "type": "boolean",
      "enum": [
        true
      ]
    },
    "error": {
      "type": "string",
      "nullable": true,
      "enum": [
        null
      ],
      "x-go-type": "rawjson.RawMessage",
      "x-go-type-import": {
        "path": "encoding/json",
        "name": "rawjson"
      }
    },
    "model": {
      "type": "string"
    },
    "mode": {
      "type": "string"
    }
  },
  "required": [
    "status",
    "healthy",
    "error",
    "model",
    "mode"
  ],
  "additionalProperties": false
}
```

### ConnectionFailure

```json
{
  "type": "object",
  "properties": {
    "status": {
      "type": "string",
      "enum": [
        "error"
      ]
    },
    "healthy": {
      "type": "boolean",
      "enum": [
        false
      ]
    },
    "error": {
      "type": "string"
    },
    "message": {
      "type": "string"
    },
    "result": {
      "type": "object",
      "properties": {
        "error": {
          "type": "string"
        }
      },
      "required": [
        "error"
      ],
      "additionalProperties": false
    },
    "model": {
      "type": "string"
    },
    "mode": {
      "type": "string"
    }
  },
  "required": [
    "status",
    "healthy",
    "error",
    "message",
    "result",
    "model",
    "mode"
  ],
  "additionalProperties": false
}
```

### ConnectionResult

```json
{
  "oneOf": [
    {
      "$ref": "#/components/schemas/ConnectionSuccess"
    },
    {
      "$ref": "#/components/schemas/ConnectionFailure"
    }
  ],
  "x-go-type": "rawjson.RawMessage",
  "x-go-type-import": {
    "path": "encoding/json",
    "name": "rawjson"
  }
}
```

### Error

```json
{
  "type": "object",
  "required": [
    "error"
  ],
  "properties": {
    "error": {
      "$ref": "#/components/schemas/ErrorDetail"
    }
  },
  "description": "Management error envelope; code matches HTTP status and failures never use a success object."
}
```

### ErrorDetail

```json
{
  "type": "object",
  "required": [
    "code",
    "message",
    "type",
    "param"
  ],
  "properties": {
    "code": {
      "type": "string",
      "pattern": "^[45][0-9]{2}$"
    },
    "message": {
      "type": "string"
    },
    "type": {
      "type": "string"
    },
    "param": {
      "nullable": true,
      "x-go-type": "json.RawMessage",
      "x-go-type-skip-optional-pointer": true,
      "description": "Currently null."
    }
  }
}
```

### Error401

```json
{
  "type": "object",
  "required": [
    "error"
  ],
  "properties": {
    "error": {
      "allOf": [
        {
          "$ref": "#/components/schemas/ErrorDetail"
        },
        {
          "type": "object",
          "required": [
            "code"
          ],
          "properties": {
            "code": {
              "type": "string",
              "pattern": "^401$"
            }
          }
        }
      ]
    }
  },
  "description": "Error fields are combined at the same object level to avoid replacing the nested error."
}
```

### Error403

```json
{
  "type": "object",
  "required": [
    "error"
  ],
  "properties": {
    "error": {
      "allOf": [
        {
          "$ref": "#/components/schemas/ErrorDetail"
        },
        {
          "type": "object",
          "required": [
            "code"
          ],
          "properties": {
            "code": {
              "type": "string",
              "pattern": "^403$"
            }
          }
        }
      ]
    }
  },
  "description": "Error fields are combined at the same object level to avoid replacing the nested error."
}
```

### Error500

```json
{
  "type": "object",
  "required": [
    "error"
  ],
  "properties": {
    "error": {
      "allOf": [
        {
          "$ref": "#/components/schemas/ErrorDetail"
        },
        {
          "type": "object",
          "required": [
            "code"
          ],
          "properties": {
            "code": {
              "type": "string",
              "pattern": "^500$"
            }
          }
        }
      ]
    }
  },
  "description": "Error fields are combined at the same object level to avoid replacing the nested error."
}
```

### Error503

```json
{
  "type": "object",
  "required": [
    "error"
  ],
  "properties": {
    "error": {
      "allOf": [
        {
          "$ref": "#/components/schemas/ErrorDetail"
        },
        {
          "type": "object",
          "required": [
            "code"
          ],
          "properties": {
            "code": {
              "type": "string",
              "pattern": "^503$"
            }
          }
        }
      ]
    }
  },
  "description": "Error fields are combined at the same object level to avoid replacing the nested error."
}
```

### Error400

```json
{
  "type": "object",
  "required": [
    "error"
  ],
  "properties": {
    "error": {
      "allOf": [
        {
          "$ref": "#/components/schemas/ErrorDetail"
        },
        {
          "type": "object",
          "required": [
            "code"
          ],
          "properties": {
            "code": {
              "type": "string",
              "pattern": "^400$"
            }
          }
        }
      ]
    }
  },
  "description": "Error fields are combined at the same object level to avoid replacing the nested error."
}
```

### Error404

```json
{
  "type": "object",
  "required": [
    "error"
  ],
  "properties": {
    "error": {
      "allOf": [
        {
          "$ref": "#/components/schemas/ErrorDetail"
        },
        {
          "type": "object",
          "required": [
            "code"
          ],
          "properties": {
            "code": {
              "type": "string",
              "pattern": "^404$"
            }
          }
        }
      ]
    }
  },
  "description": "Error fields are combined at the same object level to avoid replacing the nested error."
}
```

### Error409

```json
{
  "type": "object",
  "required": [
    "error"
  ],
  "properties": {
    "error": {
      "allOf": [
        {
          "$ref": "#/components/schemas/ErrorDetail"
        },
        {
          "type": "object",
          "required": [
            "code"
          ],
          "properties": {
            "code": {
              "type": "string",
              "pattern": "^409$"
            }
          }
        }
      ]
    }
  },
  "description": "Error fields are combined at the same object level to avoid replacing the nested error."
}
```

### PublicModelInfo

```json
{
  "type": "object",
  "properties": {
    "id": {
      "type": "string"
    },
    "transport": {
      "type": "string"
    },
    "endpoint_types": {
      "type": "array",
      "items": {
        "type": "string"
      }
    },
    "pricing_source": {
      "type": "string"
    },
    "base_model": {
      "type": "string"
    },
    "disabled": {
      "type": "boolean"
    },
    "db_model": {
      "type": "boolean"
    },
    "unavailable_reason": {
      "type": "string"
    },
    "auto_probe": {
      "type": "boolean"
    },
    "protocol_probe": {
      "$ref": "#/components/schemas/ProtocolProbeRecord"
    }
  },
  "required": [
    "id",
    "disabled",
    "db_model"
  ],
  "additionalProperties": true,
  "description": "Stable ID and status on read; extensions preserved",
  "x-go-type": "rawjson.RawMessage",
  "x-go-type-import": {
    "path": "encoding/json",
    "name": "rawjson"
  }
}
```

### DeleteSelector

```json
{
  "type": "object",
  "properties": {
    "id": {
      "type": "string"
    },
    "model_name": {
      "type": "string"
    }
  },
  "required": [],
  "additionalProperties": true,
  "x-go-type": "rawjson.RawMessage",
  "x-go-type-import": {
    "path": "encoding/json",
    "name": "rawjson"
  },
  "anyOf": [
    {
      "type": "object",
      "properties": {
        "id": {
          "type": "string"
        }
      },
      "required": [
        "id"
      ]
    },
    {
      "type": "object",
      "properties": {
        "model_name": {
          "type": "string"
        }
      },
      "required": [
        "model_name"
      ]
    }
  ]
}
```

### DeploymentPriceRate

```json
{
  "type": "object",
  "required": [
    "measure",
    "side",
    "window",
    "source_key",
    "usd",
    "unit_size"
  ],
  "properties": {
    "measure": {
      "type": "string"
    },
    "side": {
      "type": "string"
    },
    "variant": {
      "type": "string"
    },
    "window": {
      "type": "string"
    },
    "source_key": {
      "type": "string"
    },
    "label": {
      "type": "string"
    },
    "usd": {
      "type": "number",
      "minimum": 0,
      "format": "double"
    },
    "unit_size": {
      "type": "number",
      "format": "double"
    },
    "fallback": {
      "type": "boolean"
    }
  },
  "additionalProperties": true
}
```

### DeploymentPricePreview

```json
{
  "type": "object",
  "required": [
    "id",
    "name",
    "base_model",
    "manual",
    "current",
    "proposed",
    "changed",
    "reason",
    "version"
  ],
  "properties": {
    "id": {
      "type": "string"
    },
    "name": {
      "type": "string"
    },
    "base_model": {
      "type": "string"
    },
    "manual": {
      "type": "boolean"
    },
    "current": {
      "type": "array",
      "nullable": true,
      "items": {
        "$ref": "#/components/schemas/DeploymentPriceRate"
      }
    },
    "proposed": {
      "type": "array",
      "nullable": true,
      "items": {
        "$ref": "#/components/schemas/DeploymentPriceRate"
      }
    },
    "changed": {
      "type": "boolean"
    },
    "reason": {
      "type": "string",
      "enum": [
        "",
        "unmapped",
        "delisted",
        "config_model",
        "unsupported_pricing",
        "incomplete_pricing"
      ]
    },
    "version": {
      "type": "string"
    }
  },
  "additionalProperties": true
}
```

### DeploymentPriceList

```json
{
  "type": "object",
  "required": [
    "deployments",
    "generated_at"
  ],
  "properties": {
    "deployments": {
      "type": "array",
      "items": {
        "$ref": "#/components/schemas/DeploymentPricePreview"
      }
    },
    "generated_at": {
      "type": "string"
    }
  },
  "additionalProperties": true
}
```

### DeploymentPriceSelection

```json
{
  "type": "object",
  "required": [
    "items"
  ],
  "properties": {
    "items": {
      "type": "array",
      "minItems": 1,
      "maxItems": 500,
      "items": {
        "type": "object",
        "required": [
          "id",
          "version"
        ],
        "properties": {
          "id": {
            "type": "string",
            "minLength": 1
          },
          "version": {
            "type": "string",
            "minLength": 1
          }
        },
        "additionalProperties": true
      }
    }
  },
  "additionalProperties": true
}
```

### DeploymentPriceResult

```json
{
  "type": "object",
  "required": [
    "updated"
  ],
  "properties": {
    "updated": {
      "type": "integer",
      "minimum": 1,
      "maximum": 500
    }
  },
  "additionalProperties": true
}
```

### ProtocolProbeResult

```json
{
  "type": "object",
  "required": [
    "protocol",
    "status",
    "stage",
    "checked_at"
  ],
  "properties": {
    "protocol": {
      "type": "string",
      "enum": [
        "openai-chat",
        "openai-responses",
        "anthropic-messages"
      ]
    },
    "status": {
      "type": "string",
      "enum": [
        "verified",
        "unverified",
        "temporarily_failed",
        "protocol_mismatch",
        "http_error",
        "response_too_large",
        "configuration_error",
        "inconclusive"
      ]
    },
    "stage": {
      "type": "string",
      "enum": [
        "configuration",
        "reply",
        "stream",
        "tool_call",
        "tool_result",
        "complete"
      ]
    },
    "http_status": {
      "type": "integer"
    },
    "checked_at": {
      "type": "string"
    }
  }
}
```

### ProtocolProbeRecord

```json
{
  "type": "object",
  "required": [
    "checked_at",
    "results",
    "verified"
  ],
  "properties": {
    "checked_at": {
      "type": "string"
    },
    "results": {
      "type": "array",
      "items": {
        "$ref": "#/components/schemas/ProtocolProbeResult"
      }
    },
    "verified": {
      "type": "object",
      "additionalProperties": {
        "type": "boolean"
      }
    }
  },
  "additionalProperties": false
}
```
