// @flow
import TeamsContainer from './container'
import {RouteDefNode} from '../route-tree'
import NewTeamDialog from './new-team/container'
import JoinTeamDialog from './join-team/container'
import ManageChannels from '../chat/manage-channels/container'
import CreateChannel from '../chat/create-channel/container'
import ReallyLeaveTeam from './really-leave-team/container'
import RolePicker from './role-picker/container'
import Member from './team/member/container'
import Team from './team/container'
import {isMobile} from '../constants/platform'

const makeManageChannels = () => ({
  manageChannels: {
    children: {},
    component: ManageChannels,
    tags: {hideStatusBar: true, layerOnTop: !isMobile},
  },
  createChannel: {
    children: {},
    component: CreateChannel,
    tags: {hideStatusBar: true, layerOnTop: !isMobile},
  },
})

const makeRolePicker = () => ({
  rolePicker: {
    children: {},
    component: RolePicker,
    tags: {layerOnTop: !isMobile},
  },
})

const routeTree = new RouteDefNode({
  children: {
    ...makeManageChannels(),
    showNewTeamDialog: {
      children: {},
      component: NewTeamDialog,
      tags: {layerOnTop: !isMobile},
    },
    showJoinTeamDialog: {
      children: {},
      component: JoinTeamDialog,
      tags: {
        layerOnTop: !isMobile,
      },
    },
    team: {
      children: {
        ...makeManageChannels(),
        ...makeRolePicker(),
        reallyLeaveTeam: {
          children: {},
          component: ReallyLeaveTeam,
          tags: {layerOnTop: !isMobile},
        },
        member: {
          children: makeRolePicker(),
          component: Member,
        },
      },
      component: Team,
    },
  },
  component: TeamsContainer,
  tags: {title: 'Teams'},
})

export default routeTree
