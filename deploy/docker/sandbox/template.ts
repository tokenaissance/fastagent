import { Template } from 'e2b'

export const template = Template()
  .fromImage('thinkany/fastclaw-sandbox:latest')
  .setUser('root')
  .setWorkdir('/')
  .setWorkdir('/workspace')
  .setUser('user')
  .setStartCmd('sudo sleep infinity', 'sleep 20')