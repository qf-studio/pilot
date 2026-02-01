// Azure Container Apps deployment for Pilot
// https://learn.microsoft.com/en-us/azure/container-apps/
//
// Deploy:
//   az group create --name pilot-rg --location eastus
//   az deployment group create \
//     --resource-group pilot-rg \
//     --template-file deploy/azure/container-app.bicep \
//     --parameters anthropicApiKey=$ANTHROPIC_API_KEY githubToken=$GITHUB_TOKEN

@description('Anthropic API key for Claude')
@secure()
param anthropicApiKey string

@description('GitHub token for repository access')
@secure()
param githubToken string

@description('Telegram bot token (optional)')
@secure()
param telegramBotToken string = ''

@description('Telegram chat ID (optional)')
param telegramChatId string = ''

@description('Azure region for deployment')
param location string = resourceGroup().location

@description('Container image to deploy')
param containerImage string = 'ghcr.io/alekspetrov/pilot:latest'

// Container Apps Environment
resource environment 'Microsoft.App/managedEnvironments@2023-11-02-preview' = {
  name: 'pilot-env'
  location: location
  properties: {
    zoneRedundant: false
  }
}

// Storage for SQLite knowledge graph
resource storage 'Microsoft.App/managedEnvironments/storages@2023-11-02-preview' = {
  parent: environment
  name: 'pilot-storage'
  properties: {
    azureFile: {
      accountName: storageAccount.name
      accountKey: storageAccount.listKeys().keys[0].value
      shareName: fileShare.name
      accessMode: 'ReadWrite'
    }
  }
}

resource storageAccount 'Microsoft.Storage/storageAccounts@2023-01-01' = {
  name: 'pilotdata${uniqueString(resourceGroup().id)}'
  location: location
  sku: {
    name: 'Standard_LRS'
  }
  kind: 'StorageV2'
}

resource fileShare 'Microsoft.Storage/storageAccounts/fileServices/shares@2023-01-01' = {
  name: '${storageAccount.name}/default/pilot-data'
  properties: {
    shareQuota: 1
  }
}

// Pilot Container App
resource containerApp 'Microsoft.App/containerApps@2023-11-02-preview' = {
  name: 'pilot'
  location: location
  properties: {
    managedEnvironmentId: environment.id
    configuration: {
      secrets: [
        {
          name: 'anthropic-api-key'
          value: anthropicApiKey
        }
        {
          name: 'github-token'
          value: githubToken
        }
        {
          name: 'telegram-bot-token'
          value: telegramBotToken
        }
      ]
      registries: []
    }
    template: {
      containers: [
        {
          name: 'pilot'
          image: containerImage
          command: ['pilot', 'start', '--telegram', '--github']
          resources: {
            cpu: json('0.5')
            memory: '1Gi'
          }
          env: [
            {
              name: 'ANTHROPIC_API_KEY'
              secretRef: 'anthropic-api-key'
            }
            {
              name: 'GITHUB_TOKEN'
              secretRef: 'github-token'
            }
            {
              name: 'TELEGRAM_BOT_TOKEN'
              secretRef: 'telegram-bot-token'
            }
            {
              name: 'TELEGRAM_CHAT_ID'
              value: telegramChatId
            }
            {
              name: 'PILOT_DATA_DIR'
              value: '/data'
            }
          ]
          volumeMounts: [
            {
              volumeName: 'pilot-data'
              mountPath: '/data'
            }
          ]
          probes: [
            {
              type: 'Liveness'
              httpGet: {
                path: '/health'
                port: 9090
              }
              periodSeconds: 30
            }
          ]
        }
      ]
      scale: {
        minReplicas: 1
        maxReplicas: 1
      }
      volumes: [
        {
          name: 'pilot-data'
          storageName: 'pilot-storage'
          storageType: 'AzureFile'
        }
      ]
    }
  }
}

output containerAppUrl string = 'https://${containerApp.properties.configuration.ingress.fqdn}'
