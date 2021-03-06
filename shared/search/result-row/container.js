// @flow
import {Map} from 'immutable'
import {connect} from 'react-redux'
import SearchResultRow from '.'
import {followStateHelper} from '../../constants/search'

import type {TypedState} from '../../constants/reducer'
import type {SearchResultId} from '../../constants/search'

const mapStateToProps = (
  state: TypedState,
  {
    id,
    onClick,
    onMouseOver,
    onShowTracker,
  }: {id: SearchResultId, onClick: () => void, onMouseOver: () => void, onShowTracker: () => void}
) => {
  const result = state.entities.getIn(['search', 'searchResults', id], Map()).toObject()

  const leftFollowingState = followStateHelper(state, result.leftUsername, result.leftService)
  const rightFollowingState = followStateHelper(state, result.rightUsername, result.rightService)

  return {
    ...result,
    onClick,
    onMouseOver,
    onShowTracker,
    showTrackerButton: !!onShowTracker,
    leftFollowingState,
    rightFollowingState,
  }
}

export default connect(mapStateToProps)(SearchResultRow)
