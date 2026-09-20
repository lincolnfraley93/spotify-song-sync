/**
 * ${COMPONENT_NAME} component
 */

//  Imports 
import React, { Component, } from 'react';


//  Component 
export default WrappedComponent => {
  
  class ${COMPONENT_NAME} extends Component {
  
    render() {
      return <WrappedComponent
        {...this.props}
      />
    }
  }
  
  return ${COMPONENT_NAME};
}