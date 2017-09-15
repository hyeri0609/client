// @flow
import * as I from 'immutable'
import type {NoErrorTypedAction} from './types/flux'
import type {KBRecord} from './types/more'

export type LoadGit = NoErrorTypedAction<'git:loadGit', void>
export type CreateTeamRepo = NoErrorTypedAction<
  'git:createTeamRepo',
  {name: string, teamname: string, notifyTeam: boolean}
>
export type CreatePersonalRepo = NoErrorTypedAction<'git:createPersonalRepo', {name: string}>
export type SetLoading = NoErrorTypedAction<'git:setLoading', {loading: boolean}>

export type GitInfoRecord = KBRecord<{
  devicename: string,
  id: string,
  lastEditTime: string,
  lastEditUser: string,
  name: string,
  teamname: ?string,
  url: string,
}>

export const GitInfo = I.Record({
  devicename: '',
  id: '',
  lastEditTime: '',
  lastEditUser: '',
  name: '',
  teamname: null,
  url: '',
})

export const Git = I.Record({
  idToInfo: I.Map(),
  loading: true,
})

export type GitRecord = KBRecord<{
  idToInfo: I.Map<string, GitInfo>,
  loading: boolean,
}>
